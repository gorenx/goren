package companionactor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	sessionactor "github.com/gorenx/goren/session/actor"
	sessionregistry "github.com/gorenx/goren/session/registry"
	"github.com/gorenx/goren/subagent/companion"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

var errInjected = errors.New("test: rejected operation")

type faultSetup struct {
	target *goakt.GrainIdentity
	flush  bool
	close  bool
}

type faultPort struct {
	settings faultSetup
}

func (*faultPort) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*faultPort) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (endpoint *faultPort) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case faultSetup:
		endpoint.settings = input
		gc.Response(struct{}{})
		return
	case sessionactor.Flush:
		if endpoint.settings.flush {
			endpoint.settings.flush = false
			gc.Response(sessionactor.Flushed{
				Err: errInjected,
			})
			return
		}
	case agentregistry.CloseAgent:
		if endpoint.settings.close {
			endpoint.settings.close = false
			gc.Response(agentactor.Closed{
				Err: errInjected,
			})
			return
		}
	}
	reply := gc.DeferResponse()
	gc.RequestGrain(endpoint.settings.target, gc.Message(), goakt.WithRequestTimeout(0), goakt.WithReentrancyMode(reentrancy.AllowAll)).Then(func(payload any, failure error) {
		if failure != nil {
			reply.Response(response{
				err: failure,
			})
			return
		}
		reply.Response(payload)
	})
}

func faultRoute(t *testing.T, fixture *stewardship, input faultSetup) *goakt.GrainIdentity {
	t.Helper()
	route, err := goakt.GrainOf[*faultPort](t.Context(), fixture.system, "fault", goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	request[struct{}](t, fixture.system, route, input)
	return route
}

func (fixture *stewardship) reopenWith(t *testing.T, registry, conversation *goakt.GrainIdentity) {
	t.Helper()
	if err := fixture.client.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	endpoint, err := Open(t.Context(), fixture.system, Initialization{
		Parent:   fixture.input.Creation,
		Registry: registry,
		Session:  conversation,
		Creator:  fixture.creator,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.client = endpoint
}

func TestStewardRetriesOriginalIntentAfterFlushFailure(t *testing.T) {
	fixture := newStewardship(t)
	conversation := request[sessionregistry.Endpoint](t, fixture.system, fixture.sessions, sessionregistry.ResolveSession{
		SessionID:  fixture.input.Creation.AgentID,
		CreationID: fixture.input.Creation.CreationID,
	})
	port := faultRoute(t, fixture, faultSetup{
		target: conversation.Target,
		flush:  true,
	})
	fixture.reopenWith(t, fixture.authority, port)
	input := Create{
		Name: "durable",
		Initial: companion.Config{
			Enabled: true,
		},
	}
	failed, err := fixture.client.Create(t.Context(), input)
	if !errors.Is(err, errInjected) {
		t.Fatalf("Flush failure hidden: %v", err)
	}
	if failed.Persisted || failed.Creation.CreationID != "" {
		t.Fatal("unconfirmed intent published or started")
	}
	confirmed, err := fixture.client.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed.Persisted || confirmed.Companion.ChildSessionID != failed.Companion.ChildSessionID {
		t.Fatal("retry replaced original intent")
	}
	awaitPhase(t, fixture.client, "durable", Ready)
	stored, err := fixture.creator.storage.Load(t.Context(), "parent")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range stored.Events {
		if entry.Type == "companion/instance" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("retry duplicated management fact: %d", count)
	}
}

func TestStewardRetainsCloseFailureAndRetriesBeforeRestart(t *testing.T) {
	fixture := newStewardship(t)
	conversation := request[sessionregistry.Endpoint](t, fixture.system, fixture.sessions, sessionregistry.ResolveSession{
		SessionID:  fixture.input.Creation.AgentID,
		CreationID: fixture.input.Creation.CreationID,
	})
	port := faultRoute(t, fixture, faultSetup{
		target: fixture.authority,
		close:  true,
	})
	fixture.reopenWith(t, port, conversation.Target)
	if _, err := fixture.client.Create(t.Context(), Create{
		Name: "retry-close",
		Initial: companion.Config{
			Enabled: true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	ready := awaitPhase(t, fixture.client, "retry-close", Ready)
	if _, err := fixture.client.Restart(t.Context(), Restart{
		Name:               "retry-close",
		ExpectedRevision:   ready.Companion.Revision,
		ExpectedCreationID: ready.Creation.CreationID,
	}); err != nil {
		t.Fatal(err)
	}
	failed := awaitPhase(t, fixture.client, "retry-close", CloseFailed)
	if !errors.Is(failed.Err, errInjected) || failed.Creation != ready.Creation {
		t.Fatal("failed cleanup lost its creation or error")
	}
	if _, err := fixture.client.Restart(t.Context(), Restart{
		Name:               "retry-close",
		ExpectedRevision:   failed.Companion.Revision,
		ExpectedCreationID: failed.Creation.CreationID,
	}); err != nil {
		t.Fatal(err)
	}
	replacement := awaitPhase(t, fixture.client, "retry-close", Ready)
	if replacement.Creation.AgentID != ready.Creation.AgentID || replacement.Creation.CreationID == ready.Creation.CreationID {
		t.Fatal("restart bypassed old cleanup")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := fixture.client.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestStewardCloseFailureKeepsAdmissionClosedUntilCleanupSettles(t *testing.T) {
	fixture := newStewardship(t)
	conversation := request[sessionregistry.Endpoint](t, fixture.system, fixture.sessions, sessionregistry.ResolveSession{
		SessionID:  fixture.input.Creation.AgentID,
		CreationID: fixture.input.Creation.CreationID,
	})
	port := faultRoute(t, fixture, faultSetup{
		target: fixture.authority,
		close:  true,
	})
	fixture.reopenWith(t, port, conversation.Target)
	if _, err := fixture.client.Create(t.Context(), Create{
		Name: "close-retry",
		Initial: companion.Config{
			Enabled: true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	ready := awaitPhase(t, fixture.client, "close-retry", Ready)
	if err := fixture.client.Close(t.Context()); !errors.Is(err, errInjected) {
		t.Fatalf("cleanup failure hidden: %v", err)
	}
	failed := awaitPhase(t, fixture.client, "close-retry", CloseFailed)
	if failed.Creation != ready.Creation {
		t.Fatal("closing lost the retained creation")
	}
	if _, err := fixture.client.Create(t.Context(), Create{
		Name: "forbidden",
	}); !errors.Is(err, agent.ErrClosing) {
		t.Fatalf("closing reopened admission: %v", err)
	}
	if _, err := Open(t.Context(), fixture.system, Initialization{
		Parent:   fixture.input.Creation,
		Registry: port,
		Session:  conversation.Target,
		Creator:  fixture.creator,
	}); !errors.Is(err, companion.ErrConflict) {
		t.Fatalf("initialization discarded unsettled cleanup: %v", err)
	}
	if err := fixture.client.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	items, err := fixture.client.List(t.Context())
	if err != nil || len(items) != 1 || items[0].Creation.CreationID != "" {
		t.Fatalf("close retry did not settle child: %+v %v", items, err)
	}
	if _, err := fixture.client.Create(t.Context(), Create{
		Name: "forbidden",
	}); !errors.Is(err, agent.ErrClosing) {
		t.Fatalf("closed Steward accepted management: %v", err)
	}
	if err := fixture.client.Close(t.Context()); err != nil {
		t.Fatalf("completed close was not idempotent: %v", err)
	}
}
