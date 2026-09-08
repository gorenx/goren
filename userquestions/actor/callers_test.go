package questionactor

import (
	"context"
	"errors"
	"github.com/gorenx/goren/userquestions"
	"testing"
	"time"

	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/agent/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func TestCallerAuthorityRejectsUnclaimedClosedAndReplacedLifetimes(t *testing.T) {
	system, _ := questionSystem(t)
	target, err := goakt.GrainOf[*executorStub](context.Background(), system, "questions.agentloop", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	directory, err := goakt.GrainOf[*registry.Grain](ctx, system, "agents", goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	creation := registry.Creation{
		AgentID:    "questions",
		CreationID: "first",
	}
	check := func(input any) {
		t.Helper()
		payload, err := system.AskGrain(ctx, directory, input, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		switch result := payload.(type) {
		case registry.Completion:
			if result.Err != nil {
				t.Fatal(result.Err)
			}
		case agentactor.Claimed:
			if result.Err != nil {
				t.Fatal(result.Err)
			}
		case agentactor.Closed:
			if result.Err != nil {
				t.Fatal(result.Err)
			}
		case registry.CloseAccepted:
			if result.Err != nil {
				t.Fatal(result.Err)
			}
		default:
			t.Fatalf("unexpected reply %T", payload)
		}
	}
	check(registry.Reserve{
		Creation: creation,
	})
	authority, err := NewCallers(system, directory, creation, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	eligibility, err := authority.CheckCaller(ctx, "questions")
	if err != nil || eligibility.Live {
		t.Fatalf("unclaimed: %+v %v", eligibility, err)
	}
	check(agentactor.Claim{
		AgentID:    "questions",
		CreationID: "first",
		InstanceID: "activation",
		Executor:   target,
	})
	eligibility, err = authority.CheckCaller(ctx, "questions")
	if err != nil || !eligibility.Live || eligibility.Delegated {
		t.Fatalf("root: %+v %v", eligibility, err)
	}
	eligibility, err = authority.CheckCaller(ctx, "foreign")
	if err != nil || eligibility.Live {
		t.Fatalf("foreign: %+v %v", eligibility, err)
	}
	check(registry.BeginClose{
		Creation: creation,
	})
	eligibility, err = authority.CheckCaller(ctx, "questions")
	if err != nil || eligibility.Live {
		t.Fatalf("closing: %+v %v", eligibility, err)
	}
	check(registry.CloseAgent{
		Creation: creation,
	})
	check(registry.Reserve{
		Creation: registry.Creation{
			AgentID:    "questions",
			CreationID: "second",
		},
	})
	eligibility, err = authority.CheckCaller(ctx, "questions")
	if err != nil || eligibility.Live {
		t.Fatalf("replaced: %+v %v", eligibility, err)
	}
}

func TestFormerChildCanAskAsNewRuntimeRoot(t *testing.T) {
	system, _ := questionSystem(t)
	target, err := goakt.GrainOf[*executorStub](context.Background(), system, "questions.agentloop", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	directory, err := goakt.GrainOf[*registry.Grain](ctx, system, "agents", goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	parent := registry.Creation{
		AgentID:    "parent",
		CreationID: "parent-lifetime",
	}
	child := registry.Creation{
		AgentID:    "questions",
		CreationID: "child-lifetime",
	}
	for _, input := range []any{
		registry.Reserve{
			Creation: parent,
		},
		registry.Reserve{
			Creation: child,
			Parent:   &parent,
		},
		agentactor.Claim{
			AgentID:    child.AgentID,
			CreationID: child.CreationID,
			InstanceID: "child-instance",
			Executor:   target,
		},
	} {
		payload, err := system.AskGrain(ctx, directory, input, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		switch result := payload.(type) {
		case registry.Completion:
			if result.Err != nil {
				t.Fatal(result.Err)
			}
		case agentactor.Claimed:
			if result.Err != nil {
				t.Fatal(result.Err)
			}
		}
	}
	authority, err := NewCallers(system, directory, child, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	provider := &answeredProvider{}
	service := userquestions.New(provider, authority)
	input := userquestions.Request{
		SessionID: child.AgentID,
		Questions: []userquestions.Question{{
			ID:       "q",
			Question: "Continue?",
		}},
	}
	_, err = service.Ask(ctx, input)
	var problem *userquestions.Error
	if !errors.As(err, &problem) || problem.Code != userquestions.CodeDelegatedCaller || provider.calls != 0 {
		t.Fatalf("child request: %v, calls=%d", err, provider.calls)
	}
	// Close confirms the old executor has stopped before the ID is reused.
	payload, err := system.AskGrain(ctx, directory, registry.CloseAgent{
		Creation: child,
	}, time.Second)
	if err != nil || payload.(agentactor.Closed).Err != nil {
		t.Fatalf("close: %v %v", payload, err)
	}
	root := registry.Creation{
		AgentID:    child.AgentID,
		CreationID: "root-lifetime",
	}
	payload, err = system.AskGrain(ctx, directory, registry.Reserve{
		Creation: root,
	}, time.Second)
	if err != nil || payload.(registry.Completion).Err != nil {
		t.Fatalf("reserve: %v %v", payload, err)
	}
	payload, err = system.AskGrain(ctx, directory, agentactor.Claim{
		AgentID:    root.AgentID,
		CreationID: root.CreationID,
		InstanceID: "root-instance",
		Executor:   target,
	}, time.Second)
	if err != nil || payload.(agentactor.Claimed).Err != nil {
		t.Fatalf("claim: %v %v", payload, err)
	}
	_, err = service.Ask(ctx, input)
	if !errors.As(err, &problem) || problem.Code != userquestions.CodeCallerNotLive {
		t.Fatalf("old caller: %v", err)
	}
	rootAuthority, err := NewCallers(system, directory, root, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := userquestions.New(provider, rootAuthority).Ask(ctx, input)
	if err != nil || provider.calls != 1 || len(result.Answers) != 1 {
		t.Fatalf("root request: %+v %v, calls=%d", result, err, provider.calls)
	}
}

type answeredProvider struct{ calls int }

func (source *answeredProvider) Ask(context.Context, userquestions.Request) (userquestions.Answer, error) {
	source.calls++
	return userquestions.Answer{
		Answers: []userquestions.AnswerItem{{
			ID:       "q",
			Selected: []string{"yes"},
		}},
	}, nil
}

// executorStub supplies the execution close contract for caller authority tests.
type executorStub struct{}

func (*executorStub) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*executorStub) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (*executorStub) OnReceive(gc *goakt.GrainContext) {
	switch gc.Message().(type) {
	case agentactor.PrepareClose:
		gc.Response(agentactor.Prepared{})
	case agentactor.CloseResources:
		gc.Response(agentactor.Closed{})
	default:
		gc.Unhandled()
	}
}
