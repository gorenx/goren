package registry_test

import (
	"context"
	"errors"
	"testing"

	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	sessionregistry "github.com/gorenx/goren/session/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// registrationRelay corrupts only a successful registration reply. Registration
// and cleanup still execute in the real Session Registry.
type registrationRelay struct {
	target *goakt.GrainIdentity
}

func (*registrationRelay) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*registrationRelay) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *registrationRelay) OnReceive(gc *goakt.GrainContext) {
	if target, ok := gc.Message().(*goakt.GrainIdentity); ok {
		owner.target = target
		gc.Response(sessionregistry.Completion{})
		return
	}
	_, registering := gc.Message().(sessionregistry.RegisterSession)
	reply := gc.DeferResponse()
	gc.RequestGrain(owner.target, gc.Message()).Then(func(payload any, failure error) {
		if failure != nil {
			reply.Err(failure)
			return
		}
		if registering {
			result, valid := payload.(sessionregistry.Completion)
			if valid && result.Err == nil {
				reply.Response(errors.New("registration reply cannot be decoded"))
				return
			}
		}
		reply.Response(payload)
	})
}

func TestUnconfirmedSessionRegistrationStillClosesItsReservation(t *testing.T) {
	system, authority, input := creationFixture(t)
	original := input.SessionRegistry
	relay, err := goakt.GrainOf[*registrationRelay](context.Background(), system, "registration-relay",
		goakt.WithLongLivedGrain(),
		goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	askLifecycle[sessionregistry.Completion](t, system, relay, original)
	input.SessionRegistry = relay
	result := askLifecycle[agentregistry.Ready](t, system, authority, input)
	if result.Err == nil {
		t.Fatal("invalid registration reply was accepted")
	}
	if reply := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{
		Creation: input.Creation,
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	// A fresh lifetime can register only if the uncertain original was released.
	if reply := askLifecycle[sessionregistry.Completion](t, system, original, sessionregistry.RegisterSession{
		SessionID:  input.Creation.AgentID,
		CreationID: "replacement",
		Storage:    input.Storage,
	}); reply.Err != nil {
		t.Fatalf("Agent close leaked the Session reservation: %v", reply.Err)
	}
}
