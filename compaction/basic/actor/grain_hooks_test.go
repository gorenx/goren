package compactionactor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	"github.com/gorenx/goren/agentmessage"
	commandactor "github.com/gorenx/goren/commands/actor"
	"github.com/gorenx/goren/llm"
	meteractor "github.com/gorenx/goren/llm/tokenmeter/actor"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func hookFixture(t *testing.T, routed bool, supplied ...goakt.Actor) (goakt.ActorSystem, *goakt.GrainIdentity, *goakt.GrainIdentity, *goakt.GrainIdentity, <-chan string) {
	t.Helper()
	ctx := context.Background()
	system, err := goakt.NewActorSystem("compaction-hooks", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := system.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	history, err := session.NewLog("conversation", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		entry, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
			Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock(strings.Repeat("history ", 1000))},
			Source:  agentmessage.UserMessageSource{},
		})
		if err != nil {
			t.Fatal(err)
		}
		draft, err := session.NewSurfaceEventDraft(session.UserMessageAdded, entry, session.SurfaceIntent{
			Operation: session.SurfaceAppend(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := history.Commit(ctx, session.Batch(draft)); err != nil {
			t.Fatal(err)
		}
	}
	opening, err := session.NewEventDraft(session.TurnStarted, session.TurnStart{
		Turn: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.Commit(ctx, session.Batch(opening)); err != nil {
		t.Fatal(err)
	}
	if routed {
		header, err := session.NewEventDraft(session.RequestHeaderSet, session.RequestHeaderSnapshot{
			Header: session.EpochHeader{
				Config: llm.CallConfig{
					Provider: "summary",
					Model:    "checkpoint",
				},
			},
			Reason: session.RequestHeaderInitial,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := history.Commit(ctx, session.Batch(header)); err != nil {
			t.Fatal(err)
		}
	}
	conversation, err := goakt.GrainOf[*historyFixture](ctx, system, "conversation", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := system.AskGrain(ctx, conversation, bindHistory{
		history:  history,
		flushing: make(chan *goakt.GrainReply, 1),
	}, time.Second); err != nil {
		t.Fatal(err)
	}
	meter, err := goakt.GrainOf[*meteractor.Grain](ctx, system, meteractor.RuntimeName("conversation"), goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	calls := make(chan string, 8)
	var provider goakt.Actor = &modelFixture{
		calls: calls,
	}
	if len(supplied) != 0 {
		provider = supplied[0]
	}
	router, err := system.Spawn(ctx, "model", provider, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	target, err := goakt.GrainOf[*Grain](ctx, system, RuntimeName("conversation"), goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := goakt.GrainOf[*commandactor.Registry](ctx, system, commandactor.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	response, err := system.AskGrain(ctx, target, Initialize{
		Commands:   catalog,
		Agent:      target,
		AgentID:    "conversation",
		Session:    conversation,
		CreationID: "creation",
		Meter:      meter,
		Router:     router,
		Timeout:    100 * time.Millisecond,
	}, time.Second)
	if err != nil || response.(Completion).Err != nil {
		t.Fatalf("initialize: %#v, %v", response, err)
	}
	coordinator, err := goakt.GrainOf[*middlewareactor.Grain](ctx, system, "middleware", goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	return system, target, coordinator, conversation, calls
}

func TestPressureMiddlewareContinuesWithoutRouteOrAfterMetadataFailure(t *testing.T) {
	for _, routed := range []bool{false, true} {
		system, target, coordinator, _, calls := hookFixture(t, routed)
		response, err := system.AskGrain(context.Background(), coordinator, middlewareactor.ResolvePreStep{
			CallID: "pressure",
			Routes: []middlewareactor.Route{{
				Definition: "compaction",
				Target:     target,
			}},
			Input: agent.PreStepInput{
				AgentID: "conversation",
				Turn:    1,
				Step:    1,
			},
			Default: agent.PreStepDecision{
				Kind: agent.PreStepEnter,
			},
		}, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		result := response.(agent.PreStepResolved)
		if result.Err != nil || result.Decision.Kind != agent.PreStepEnter {
			t.Fatalf("pre-step: %#v", result)
		}
		select {
		case call := <-calls:
			t.Fatalf("unexpected summary: %s", call)
		default:
		}
	}
}

func TestOverflowMiddlewareRetriesOnlyAfterCommittedProgress(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		system, target, coordinator, conversation, calls := hookFixture(t, true)
		code := "other"
		if overflow {
			code = llm.ContextWindowExceededCode
		}
		response, err := system.AskGrain(context.Background(), coordinator, middlewareactor.ResolveRequestError{
			CallID: "overflow",
			Routes: []middlewareactor.Route{{
				Definition: "compaction",
				Target:     target,
			}},
			Input: agent.RequestFailure{
				AgentID: "conversation",
				Turn:    1,
				Step:    1,
				Failure: llm.LlmFailure{
					Code: code,
				},
			},
		}, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		result := response.(agent.RequestErrorResolved)
		if result.Err != nil || result.Action.Retry != overflow {
			t.Fatalf("request error: %#v", result)
		}
		response, err = system.AskGrain(context.Background(), conversation, sessionactor.Read{}, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		snapshot := response.(sessionactor.View).Snapshot
		if (snapshot.Surface.ReplaceGeneration > 0) != overflow {
			t.Fatal("retry did not match committed replacement")
		}
		if overflow {
			if first, second := <-calls, <-calls; first != "generation" || second != "close" {
				t.Fatalf("model lifecycle: %s, %s", first, second)
			}
		} else {
			select {
			case call := <-calls:
				t.Fatalf("unexpected recovery: %s", call)
			default:
			}
		}
	}
}
