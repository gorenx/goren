package companionactor

import (
	"context"
	"errors"
	"testing"
	"time"

	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func installParent(t *testing.T, fixture *stewardship, authority *goakt.GrainIdentity, creators ...ChildCreator) (*goakt.GrainIdentity, *Client, agentregistry.CreateAgent) {
	t.Helper()
	var starter ChildCreator = fixture.creator
	if len(creators) != 0 {
		starter = creators[0]
	}
	endpoint, err := Register(t.Context(), fixture.system, Dependencies{
		Registry: authority,
		Settings: fixture.creator.settings,
		Creator:  starter,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := fixture.input
	input.Creation = agentregistry.Creation{
		AgentID:    "managed-parent",
		CreationID: "managed-first",
	}
	input.Components = []*goakt.GrainIdentity{endpoint}
	ready := request[agentregistry.Ready](t, fixture.system, fixture.authority, input)
	if ready.Err != nil {
		t.Fatal(ready.Err)
	}
	if len(ready.Components) != 1 {
		t.Fatal("component ownership absent from Ready")
	}
	sender := &Client{
		system: fixture.system,
		target: ready.Components[0].Runtime,
		parent: input.Creation,
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		closed, failure := ask[agentactor.Closed](ctx, fixture.system, fixture.authority, agentregistry.CloseAgent{
			Creation: input.Creation,
		})
		if failure != nil || closed.Err != nil && !errors.Is(closed.Err, agentregistry.ErrCreation) {
			t.Errorf("managed parent cleanup: %v %v", failure, closed.Err)
		}
	})
	return endpoint, sender, input
}

func awaitInstallation(t *testing.T, fixture *stewardship, endpoint *goakt.GrainIdentity, parent agentregistry.Creation, expected Phase) Status {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := ask[Status](ctx, fixture.system, endpoint, InspectInstallation{
			Parent: parent,
		})
		if err != nil {
			t.Fatal(err)
		}
		if current.Phase == expected {
			return current
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("installation did not reach %s: %+v", expected, current)
		}
	}
}

func TestInstallerParentCloseSettlesChildren(t *testing.T) {
	for _, stage := range []string{"ready", "preparing"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newStewardship(t)
			endpoint, sender, input := installParent(t, fixture, fixture.authority)
			awaitInstallation(t, fixture, endpoint, input.Creation, Ready)
			var gate *creationGate
			if stage == "preparing" {
				gate = &creationGate{
					entered:  make(chan struct{}),
					canceled: make(chan struct{}),
					release:  make(chan struct{}),
				}
				t.Cleanup(func() {
					select {
					case <-gate.release:
					default:
						close(gate.release)
					}
				})
				fixture.creator.mu.Lock()
				fixture.creator.gate = gate
				fixture.creator.mu.Unlock()
			}
			created, err := sender.Create(t.Context(), Create{
				Name:    "reviewer",
				Enabled: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if gate != nil {
				select {
				case <-gate.entered:
				case <-ctx.Done():
					t.Fatal("child preparation not reached")
				}
			} else {
				child := awaitPhase(t, sender, "reviewer", Ready)
				// Child inherits ordinary component wiring, but Companion role must skip a nested Steward.
				nested := request[Status](t, fixture.system, endpoint, InspectInstallation{
					Parent: child.Creation,
				})
				if !errors.Is(nested.Err, agentregistry.ErrCreation) {
					t.Fatal("Companion installed a nested Steward")
				}
			}
			// Child preparation never holds the parent Ready barrier.
			available := request[agentregistry.Ready](t, fixture.system, fixture.authority, agentregistry.FindAgent{
				AgentID: input.Creation.AgentID,
			})
			if available.Err != nil {
				t.Fatal(available.Err)
			}
			finished := make(chan error, 1)
			go func() {
				closed, failure := ask[agentactor.Closed](ctx, fixture.system, fixture.authority, agentregistry.CloseAgent{
					Creation: input.Creation,
				})
				finished <- errors.Join(failure, closed.Err)
			}()
			if gate != nil {
				select {
				case <-gate.canceled:
				case <-ctx.Done():
					t.Fatal("StopComponent did not cancel preparation")
				}
				select {
				case failure := <-finished:
					t.Fatalf("close abandoned pending creation: %v", failure)
				default:
				}
				close(gate.release)
			}
			select {
			case failure := <-finished:
				if failure != nil {
					t.Fatal(failure)
				}
			case <-ctx.Done():
				t.Fatal("parent close did not settle")
			}
			current := awaitInstallation(t, fixture, endpoint, input.Creation, Stopped)
			if current.Err != nil {
				t.Fatal(current.Err)
			}
			remaining := request[agentregistry.Ready](t, fixture.system, fixture.authority, agentregistry.FindAgent{
				AgentID: created.Companion.ChildSessionID,
			})
			if remaining.Err == nil {
				t.Fatal("child survived confirmed parent close")
			}
		})
	}
}

// preparationBarrier delays only the component's readiness lookup. The parent
// uses the real Registry, proving independent installation and close progress.
type preparationBarrier struct {
	entered chan struct{}
	reply   *goakt.GrainReply
}

type delayPreparation struct {
	entered chan struct{}
}
type failPreparation struct {
	err error
}

func (*preparationBarrier) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*preparationBarrier) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *preparationBarrier) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case delayPreparation:
		owner.entered = input.entered
		gc.Response(struct{}{})
	case agentregistry.FindAgent:
		owner.reply = gc.DeferResponse()
		close(owner.entered)
	case failPreparation:
		owner.reply.Response(agentregistry.Ready{
			Err: input.err,
		})
		owner.reply = nil
		gc.Response(struct{}{})
	default:
		gc.Unhandled()
	}
}

func TestInstallerPreparationDoesNotBlockParent(t *testing.T) {
	for _, outcome := range []string{"cancel", "failure"} {
		t.Run(outcome, func(t *testing.T) {
			fixture := newStewardship(t)
			barrier, err := goakt.GrainOf[*preparationBarrier](t.Context(), fixture.system, "preparation-barrier", goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
			if err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			request[struct{}](t, fixture.system, barrier, delayPreparation{
				entered: entered,
			})
			endpoint, sender, input := installParent(t, fixture, barrier)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("component preparation did not begin")
			}
			awaitInstallation(t, fixture, endpoint, input.Creation, Starting)
			direct, statusErr := sender.Status(t.Context())
			if statusErr != nil || direct.Phase != Starting {
				t.Fatalf("Steward does not own preparation status: %+v %v", direct, statusErr)
			}

			failure := errors.New("configuration unavailable")
			if outcome == "failure" {
				request[struct{}](t, fixture.system, barrier, failPreparation{
					err: failure,
				})
				current := awaitInstallation(t, fixture, endpoint, input.Creation, StartFailed)
				direct, statusErr = sender.Status(t.Context())
				if statusErr != nil || direct.Phase != StartFailed || !errors.Is(direct.Err, failure) {
					t.Fatalf("Steward lost initialization failure: %+v %v", direct, statusErr)
				}
				if !errors.Is(current.Err, failure) {
					t.Fatalf("lost preparation failure: %v", current.Err)
				}
				ready := request[agentregistry.Ready](t, fixture.system, fixture.authority, agentregistry.FindAgent{
					AgentID: input.Creation.AgentID,
				})
				if ready.Err != nil {
					t.Fatal("component failure revoked parent readiness", ready.Err)
				}
			}
			closed := request[agentactor.Closed](t, fixture.system, fixture.authority, agentregistry.CloseAgent{
				Creation: input.Creation,
			})
			if closed.Err != nil {
				t.Fatal(closed.Err)
			}
			awaitInstallation(t, fixture, endpoint, input.Creation, Stopped)
			if outcome == "cancel" {
				request[struct{}](t, fixture.system, barrier, failPreparation{
					err: failure,
				})
			}
			// A late reply and a stale caller cannot reopen the stopped component.
			stale := request[Status](t, fixture.system, endpoint, InspectInstallation{
				Parent: agentregistry.Creation{
					AgentID:    input.Creation.AgentID,
					CreationID: "stale",
				},
			})
			if !errors.Is(stale.Err, agentregistry.ErrCreation) {
				t.Fatal("stale creation inspected current component")
			}
		})
	}
}

func TestInstallerReplacementRejectsOldCreation(t *testing.T) {
	fixture := newStewardship(t)
	endpoint, sender, input := installParent(t, fixture, fixture.authority)
	awaitInstallation(t, fixture, endpoint, input.Creation, Ready)
	if _, err := sender.Create(t.Context(), Create{
		Name:    "reviewer",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	previous := awaitPhase(t, sender, "reviewer", Ready)
	closed := request[agentactor.Closed](t, fixture.system, fixture.authority, agentregistry.CloseAgent{
		Creation: input.Creation,
	})
	if closed.Err != nil {
		t.Fatal(closed.Err)
	}
	old := input.Creation
	input.Creation.CreationID = "managed-replacement"
	input.Restore = true
	ready := request[agentregistry.Ready](t, fixture.system, fixture.authority, input)
	if ready.Err != nil {
		t.Fatal(ready.Err)
	}
	// The replacement owns a separate cleanup request; the original fixture
	// can no longer release it through the old creation credential.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		current, failure := ask[agentactor.Closed](ctx, fixture.system, fixture.authority, agentregistry.CloseAgent{
			Creation: input.Creation,
		})
		if failure != nil || current.Err != nil {
			t.Errorf("replacement close: %v %v", failure, current.Err)
		}
	})
	awaitInstallation(t, fixture, endpoint, input.Creation, Ready)
	active := &Client{
		system: fixture.system,
		target: ready.Components[0].Runtime,
		parent: input.Creation,
	}
	restored := awaitPhase(t, active, "reviewer", Ready)
	if restored.Companion.ChildSessionID != previous.Companion.ChildSessionID || restored.Companion.Configuration != previous.Companion.Configuration || restored.Creation == previous.Creation {
		t.Fatal("replacement failed to recover the same child Session and configuration with a new Creation")
	}
	stale := request[agentactor.ComponentStopped](t, fixture.system, endpoint, agentactor.StopComponent{
		AgentID:    old.AgentID,
		CreationID: old.CreationID,
	})
	if !errors.Is(stale.Err, agentregistry.ErrCreation) {
		t.Fatalf("stale stop reached replacement: %v", stale.Err)
	}
	if _, err := sender.Create(t.Context(), Create{
		Name: "stale",
	}); !errors.Is(err, agentregistry.ErrCreation) {
		t.Fatalf("old Client reached replacement: %v", err)
	}
	awaitInstallation(t, fixture, endpoint, input.Creation, Ready)
}
