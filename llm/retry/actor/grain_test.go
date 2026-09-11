package retryactor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	"github.com/gorenx/goren/llm"
	llmretry "github.com/gorenx/goren/llm/retry"
	retryactor "github.com/gorenx/goren/llm/retry/actor"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type setup struct {
	history   *session.Log
	scheduled chan struct{}
	hold      bool
}
type release struct{}
type storage struct {
	history   *session.Log
	scheduled chan struct{}
	hold      bool
	reply     *goakt.GrainReply
	result    sessionactor.Committed
}

func (*storage) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*storage) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *storage) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case setup:
		owner.history = input.history
		owner.scheduled = input.scheduled
		owner.hold = input.hold
		gc.Response(struct{}{})
	case sessionactor.Read:
		gc.Response(sessionactor.View{
			Snapshot: owner.history.Snapshot(),
		})
	case sessionactor.Commit:
		write, err := owner.history.Commit(gc.Context(), input.Plan)
		result := sessionactor.Committed{
			Result: write,
			Err:    err,
		}
		if err == nil && len(write.Events) == 1 && write.Events[0].Type == llmretry.RetryEventName {
			close(owner.scheduled)
			if owner.hold {
				owner.reply = gc.DeferResponse()
				owner.result = result
				return
			}
		}
		gc.Response(result)
	case release:
		owner.reply.Response(owner.result)
		owner.reply = nil
		gc.Response(struct{}{})
	}
}

func makeHistory(t *testing.T) *session.Log {
	t.Helper()
	history, err := session.NewLog("conversation", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	started, err := session.NewEventDraft(session.TurnStarted, session.TurnStart{
		Turn: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	step, err := session.NewEventDraft(session.StepStarted, session.StepPosition{
		Turn: 1,
		Step: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	header, err := session.NewEventDraft(session.RequestHeaderSet, session.RequestHeaderSnapshot{
		Header: session.EpochHeader{
			Config: llm.CallConfig{
				Provider: "p",
				Model:    "m",
			},
		},
		Reason: session.RequestHeaderInitial,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.Commit(context.Background(), session.Batch(started, step, header)); err != nil {
		t.Fatal(err)
	}
	return history
}

func TestRecoveryCancellationWaitsForCommitAndDoesNotStartRetry(t *testing.T) {
	for _, hold := range []bool{false, true} {
		t.Run(map[bool]string{false: "wait", true: "commit"}[hold], func(t *testing.T) {
			ctx := context.Background()
			system, err := goakt.NewActorSystem("retry-test", goakt.WithLogger(log.DiscardLogger))
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
			options := []goakt.GrainOption{goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll)))}
			conversation, err := goakt.GrainOf[*storage](ctx, system, "storage", options...)
			if err != nil {
				t.Fatal(err)
			}
			scheduled := make(chan struct{})
			if _, err := system.AskGrain(ctx, conversation, setup{
				history:   makeHistory(t),
				scheduled: scheduled,
				hold:      hold,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			target, err := goakt.GrainOf[*retryactor.Grain](ctx, system, "retry", options...)
			if err != nil {
				t.Fatal(err)
			}
			initialized, err := system.AskGrain(ctx, target, retryactor.Initialize{
				AgentID:    "conversation",
				Session:    conversation,
				CreationID: "creation",
			}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if err := initialized.(retryactor.Completion).Err; err != nil {
				t.Fatal(err)
			}
			replies := make(chan agent.RequestErrorResolved, 1)
			go func() {
				payload, failure := system.AskGrain(ctx, target, middlewareactor.InvokeRequestError{
					Invocation: middlewareactor.Invocation{
						CallID:      "request",
						Handler:     "retry",
						Coordinator: conversation,
					},
					Input: agent.RequestFailure{
						AgentID:  "conversation",
						Turn:     1,
						Step:     1,
						Provider: "p",
						Failure: llm.LlmFailure{
							Message: "busy",
							Code:    "SERVER",
						},
						RetryPolicy: llm.NormalRetryPolicy{
							Mode:           llm.RetryNormal,
							MaxRetries:     1,
							RetryableCodes: []string{"SERVER"},
							ResolvedRetryBackoff: llm.ResolvedRetryBackoff{
								InitialDelayMS: 10000,
								MaxDelayMS:     10000,
								JitterRatio:    0,
							},
						},
					},
				}, 3*time.Second)
				result, ok := payload.(agent.RequestErrorResolved)
				if failure != nil {
					result.Err = failure
				} else if !ok {
					result.Err = errors.New("invalid recovery reply")
				}
				replies <- result
			}()
			select {
			case <-scheduled:
			case <-time.After(time.Second):
				t.Fatal("retry schedule was not committed")
			}
			cause := errors.New("parent canceled")
			acknowledged, err := system.AskGrain(ctx, target, middlewareactor.CancelInvocation{
				CallID:  "request",
				Handler: "retry",
				Cause:   cause,
			}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if err := acknowledged.(middlewareactor.Completion).Err; err != nil {
				t.Fatal(err)
			}
			if hold {
				select {
				case result := <-replies:
					t.Fatalf("returned before original commit reply: %+v", result)
				default:
				}
				if _, err := system.AskGrain(ctx, conversation, release{}, time.Second); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case result := <-replies:
				if result.Action.Retry || !errors.Is(result.Err, cause) {
					t.Fatalf("canceled recovery: %+v", result)
				}
			case <-time.After(time.Second):
				t.Fatal("canceled recovery did not exit")
			}
			payload, err := system.AskGrain(ctx, conversation, sessionactor.Read{}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			entries := payload.(sessionactor.View).Snapshot.Events
			if len(entries) != 4 || entries[3].Type != llmretry.RetryEventName {
				t.Fatalf("unexpected retry facts: %+v", entries)
			}
		})
	}
}
