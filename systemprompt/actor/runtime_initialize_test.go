package promptactor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	"github.com/gorenx/goren/tools"
	toolactor "github.com/gorenx/goren/tools/runtime/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type promptDefinitions struct {
	agentID  session.SessionID
	snapshot systemprompt.Snapshot
}

type initializationRegistry struct {
	agentID  session.SessionID
	snapshot systemprompt.Snapshot
}

func (*initializationRegistry) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*initializationRegistry) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *initializationRegistry) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case promptDefinitions:
		owner.snapshot = input.snapshot
		owner.agentID = input.agentID
		gc.Response(struct{}{})
	case promptactor.Prepare:
		gc.Response(promptactor.Prepared{
			Version:  1,
			Snapshot: owner.snapshot,
		})
	case toolactor.ReadDefinitions:
		gc.Response(toolactor.Definitions{
			Value: tools.Definitions{
				AgentID: owner.agentID,
			},
		})
	default:
		gc.Unhandled()
	}
}

type runtimeInitialization struct {
	target *goakt.GrainIdentity
	input  promptactor.InitializeRuntime
}

type initializationReply struct {
	payload any
	err     error
}

type initializationCaller struct {
	replies chan initializationReply
}

func (*initializationCaller) PreStart(*goakt.Context) error { return nil }
func (*initializationCaller) PostStop(*goakt.Context) error { return nil }
func (owner *initializationCaller) Receive(rc *goakt.ReceiveContext) {
	if input, ok := rc.Message().(runtimeInitialization); ok {
		replies := owner.replies
		rc.RequestGrain(input.target, input.input).Then(func(payload any, failure error) {
			replies <- initializationReply{
				payload: payload,
				err:     failure,
			}
		})
	}
}

// initializeRuntime exercises registry requests instead of injecting resolved
// state into the Runtime. A nil baseline provides an empty committed catalog.
func initializeRuntime(t *testing.T, ctx context.Context, system goakt.ActorSystem, target *goakt.GrainIdentity, identifier session.SessionID, definitions systemprompt.Snapshot, baseline *goakt.GrainIdentity) (any, error) {
	t.Helper()
	registry, err := goakt.GrainOf[*initializationRegistry](ctx, system, "initialization-"+uuid.NewString(), goakt.WithLongLivedGrain())
	if err != nil {
		return nil, err
	}
	if _, err := system.AskGrain(ctx, registry, promptDefinitions{
		snapshot: definitions,
		agentID:  identifier,
	}, time.Second); err != nil {
		return nil, err
	}
	if baseline == nil {
		baseline = registry
	}
	replies := make(chan initializationReply, 1)
	caller, err := system.Spawn(ctx, "initialize-"+uuid.NewString(), &initializationCaller{
		replies: replies,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		return nil, err
	}
	if err := goakt.Tell(ctx, caller, runtimeInitialization{
		target: target,
		input: promptactor.InitializeRuntime{
			AgentID:        identifier,
			PromptRegistry: registry,
			ToolsRuntime:   baseline,
			CreationID:     "creation",
		},
	}); err != nil {
		return nil, err
	}
	select {
	case result := <-replies:
		return result.payload, result.err
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-time.After(3 * time.Second):
		return nil, errors.New("prompt initialization timed out")
	}
}
