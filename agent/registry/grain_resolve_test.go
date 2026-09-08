package registry_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	configactor "github.com/gorenx/goren/llm/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// resolutionProbe sends an admission query after Resolve on the same sender.
// Its acknowledgement proves the Registry has processed the waiting request.
type resolutionProbe struct {
	target   *goakt.GrainIdentity
	creation agentregistry.Creation
	results  chan response
	checked  chan error
}

func (*resolutionProbe) PreStart(*goakt.Context) error { return nil }
func (*resolutionProbe) PostStop(*goakt.Context) error { return nil }
func (probe *resolutionProbe) Receive(rc *goakt.ReceiveContext) {
	if _, ok := rc.Message().(agentregistry.Resolve); !ok {
		return
	}
	rc.RequestGrain(probe.target, rc.Message()).Then(func(payload any, failure error) {
		probe.results <- response{
			value: payload,
			err:   failure,
		}
	})
	rc.RequestGrain(probe.target, agentregistry.CheckAdmission{
		Creation: probe.creation,
	}).Then(func(payload any, failure error) {
		if failure == nil {
			failure = payload.(agentregistry.Completion).Err
		}
		probe.checked <- failure
	})
}

func TestResolveWaitsForExistingCreation(t *testing.T) {
	for _, scenario := range []string{"ready", "configuration failure", "closing"} {
		t.Run(scenario, func(t *testing.T) {
			system, authority, input := creationFixture(t)
			missing := askLifecycle[agentregistry.Ready](t, system, authority, agentregistry.Resolve{
				AgentID:   input.Creation.AgentID,
				WaitReady: true,
			})
			if !errors.Is(missing.Err, agentregistry.ErrCreation) {
				t.Fatalf("missing = %+v", missing)
			}
			held := make(chan *goakt.GrainReply, 1)
			askLifecycle[struct{}](t, system, input.ModelConfig, holdConfiguration{
				replies: held,
			})
			created := make(chan response, 1)
			go func() {
				payload, failure := system.AskGrain(context.Background(), authority, input, 3*time.Second)
				created <- response{
					value: payload,
					err:   failure,
				}
			}()
			var pending *goakt.GrainReply
			select {
			case pending = <-held:
			case <-time.After(time.Second):
				t.Fatal("configuration not requested")
			}
			immediate := askLifecycle[agentregistry.Ready](t, system, authority, agentregistry.Resolve{
				AgentID: input.Creation.AgentID,
			})
			if immediate.Err == nil {
				t.Fatal("default Resolve published incomplete creation")
			}
			results := make(chan response, 1)
			checked := make(chan error, 1)
			probe, err := system.Spawn(context.Background(), "resolution-probe", &resolutionProbe{
				target:   authority,
				creation: input.Creation,
				results:  results,
				checked:  checked,
			}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
			if err != nil {
				t.Fatal(err)
			}
			if err := goakt.Tell(context.Background(), probe, agentregistry.Resolve{
				AgentID:   input.Creation.AgentID,
				WaitReady: true,
			}); err != nil {
				t.Fatal(err)
			}
			select {
			case failure := <-checked:
				if failure != nil {
					t.Fatal(failure)
				}
			case <-time.After(time.Second):
				t.Fatal("Registry did not process waiting request")
			}
			select {
			case result := <-results:
				t.Fatalf("Resolve replied before creation completed: %+v", result)
			default:
			}
			var failure error
			if scenario == "configuration failure" {
				failure = errors.New("configuration unavailable")
			}
			if scenario == "closing" {
				accepted := askLifecycle[agentregistry.CloseAccepted](t, system, authority, agentregistry.BeginClose{
					Creation: input.Creation,
				})
				if accepted.Err != nil {
					t.Fatal(accepted.Err)
				}
				refused := askLifecycle[agentregistry.Ready](t, system, authority, agentregistry.Resolve{
					AgentID:   input.Creation.AgentID,
					WaitReady: true,
				})
				if !errors.Is(refused.Err, agent.ErrClosing) {
					t.Fatalf("new waiter during close = %+v", refused)
				}
				failure = agent.ErrClosing
			}
			answer := configactor.Completion{}
			if scenario == "configuration failure" {
				answer.Err = failure
			}
			pending.Response(answer)
			for _, arrivals := range []<-chan response{created, results} {
				select {
				case result := <-arrivals:
					if result.err != nil {
						t.Fatal(result.err)
					}
					ready := result.value.(agentregistry.Ready)
					if !errors.Is(ready.Err, failure) {
						t.Fatalf("result = %+v, want %v", ready, failure)
					}
					if failure != nil && (ready.Executor != nil || ready.Commands != nil) {
						t.Fatal("failure exposed executable route")
					}
					if failure == nil && (ready.Executor == nil || ready.Creation != input.Creation) {
						t.Fatal("wrong lifetime")
					}
				case <-time.After(time.Second):
					t.Fatal("waiting caller did not receive final result")
				}
			}
			closed := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{
				Creation: input.Creation,
			})
			if closed.Err != nil {
				t.Fatal(closed.Err)
			}
		})
	}
}
