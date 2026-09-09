package loopactor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	"github.com/gorenx/goren/agentloop"
	stepstate "github.com/gorenx/goren/agentloop/internal/step"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type stoppingSetup struct {
	failCancel bool
	phase      string
	entered    chan struct{}
}
type releaseStopping struct{}
type stoppingEndpoint struct {
	failCancel bool
	phase      string
	entered    chan struct{}
	reply      *goakt.GrainReply
	id         middlewareactor.CallID
	cause      error
}

func (*stoppingEndpoint) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*stoppingEndpoint) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *stoppingEndpoint) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case stoppingSetup:
		if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))); err != nil {
			panic(err)
		}
		owner.entered = input.entered
		owner.phase = input.phase
		owner.failCancel = input.failCancel
		gc.Response(struct{}{})
	case promptactor.Assemble:
		if owner.phase != "prompt" {
			gc.Response(promptactor.Assembled{
				ID: input.ID,
			})
			return
		}
		owner.reply = gc.DeferResponse()
		owner.id = middlewareactor.CallID(input.ID)
		close(owner.entered)
	case middlewareactor.ResolvePreStep:
		owner.reply = gc.DeferResponse()
		owner.id = input.CallID
		close(owner.entered)
	case promptactor.CancelAssembly:
		if middlewareactor.CallID(input.ID) != owner.id {
			panic("wrong Prompt invocation")
		}
		owner.cause = errors.New("Prompt canceled")
		gc.Response(promptactor.Completion{})
	case middlewareactor.InvokeTurnStopping:
		owner.reply = gc.DeferResponse()
		owner.id = input.Invocation.CallID
		close(owner.entered)
	case middlewareactor.CancelInvocation:
		if input.CallID != owner.id || input.Handler != "stopping" {
			panic("wrong middleware cancellation")
		}
		if owner.cause != nil && owner.cause.Error() != input.Cause.Error() {
			panic("retry replaced the first cancellation cause")
		}
		owner.cause = input.Cause
		if owner.failCancel {
			owner.failCancel = false
			gc.Response(middlewareactor.Completion{
				Err: errors.New("test: cancellation delivery failed"),
			})
			return
		}
		gc.Response(middlewareactor.Completion{})
	case middlewareactor.RunTurnStopping:
		owner.reply = gc.DeferResponse()
		owner.id = input.CallID
		close(owner.entered)
	case middlewareactor.CancelCall:
		if input.CallID != owner.id {
			panic("wrong stopping invocation")
		}
		owner.cause = input.Cause
		gc.Response(middlewareactor.Completion{})
	case releaseStopping:
		switch owner.phase {
		case "prompt":
			owner.reply.Response(promptactor.Assembled{
				ID:  string(owner.id),
				Err: owner.cause,
			})
		case "pre-step":
			owner.reply.Response(agent.PreStepResolved{
				Err: owner.cause,
			})
		default:
			owner.reply.Response(agent.TurnStoppingCompleted{
				Err: owner.cause,
			})
		}
		owner.reply = nil
		gc.Response(struct{}{})
	}
}

type continuationRequest struct {
	routes []middlewareactor.Route
	target *goakt.GrainIdentity
	hooks  *goakt.GrainIdentity
}
type continuationCaller struct {
	replies chan turnContinued
}

func (*continuationCaller) PreStart(*goakt.Context) error { return nil }
func (*continuationCaller) PostStop(*goakt.Context) error { return nil }
func (owner *continuationCaller) Receive(rc *goakt.ReceiveContext) {
	if input, ok := rc.Message().(continuationRequest); ok {
		replies := owner.replies
		rc.RequestGrain(input.target, continueTurn{
			middleware: input.hooks,
			routes:     input.routes,
		}).Then(func(payload any, failure error) {
			result, ok := payload.(turnContinued)
			if failure != nil {
				result.err = failure
			} else if !ok {
				result.err = errors.New("unexpected continuation response")
			}
			replies <- result
		})
	}
}

func TestStoppingWaitAcceptsSteerAndWaitsForCanceledHook(t *testing.T) {
	for _, scenario := range []string{"steer", "cancel", "retry cancellation"} {
		t.Run(scenario, func(t *testing.T) {
			canceled := scenario != "steer"
			retry := scenario == "retry cancellation"
			ctx := context.Background()
			system, err := goakt.NewActorSystem("stopping", goakt.WithLogger(log.DiscardLogger))
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
			subject, history := preparedTurn(t)
			finishStepForStopping(t, subject, history)
			storage, err := goakt.GrainOf[*settlementStorage](ctx, system, "storage", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := system.AskGrain(ctx, storage, storageSetup{
				history: history,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			target, err := goakt.GrainOf[*settlementAgent](ctx, system, "execution", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := system.AskGrain(ctx, target, settlementSetup{
				subject: subject,
				target:  storage,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			hooks, err := goakt.GrainOf[*stoppingEndpoint](ctx, system, "hooks", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			if _, err := system.AskGrain(ctx, hooks, stoppingSetup{
				entered:    entered,
				failCancel: retry,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			coordinator, err := goakt.GrainOf[*middlewareactor.Grain](ctx, system, "coordinator",
				goakt.WithLongLivedGrain(),
				goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))),
			)
			if err != nil {
				t.Fatal(err)
			}
			replies := make(chan turnContinued, 1)
			caller, err := system.Spawn(ctx, "caller", &continuationCaller{
				replies: replies,
			}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
			if err != nil {
				t.Fatal(err)
			}
			if err := goakt.Tell(ctx, caller, continuationRequest{
				target: target,
				hooks:  coordinator,
				routes: []middlewareactor.Route{
					{
						Definition: "stopping",
						Target:     hooks,
					},
				},
			}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("hook did not start")
			}
			message, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
				Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("steer during hook")},
				Source:  agentmessage.UserMessageSource{},
			})
			if err != nil {
				t.Fatal(err)
			}
			payload, err := system.AskGrain(ctx, target, Steer{
				CreationID: "test",
				Message:    message,
			}, time.Second)
			if err != nil || payload.(Changed).Err != nil {
				t.Fatalf("Steer blocked: %v, %v", payload, err)
			}
			if canceled {
				payload, err = system.AskGrain(ctx, target, Cancel{
					CreationID: "test",
					Cause:      agent.UserCancel{},
					Options: agent.CancelOptions{
						KeepInbox: true,
					},
				}, time.Second)
				if err != nil || (payload.(Changed).Err != nil) != retry {
					t.Fatalf("Cancel acknowledgement: %v, %v", payload, err)
				}
				if retry {
					payload, err = system.AskGrain(ctx, target, Cancel{
						CreationID: "test",
						Cause:      agent.ParentCancel{},
						Options: agent.CancelOptions{
							KeepInbox: true,
						},
					}, time.Second)
					if err != nil || payload.(Changed).Err != nil {
						t.Fatalf("Cancel retry: %v, %v", payload, err)
					}
				}
			}
			payload, err = system.AskGrain(ctx, target, finishTurn{}, time.Second)
			if err != nil || payload.(turnFinished).err == nil {
				t.Fatalf("settlement bypassed hook: %v, %v", payload, err)
			}
			select {
			case result := <-replies:
				t.Fatalf("hook completed before release: %+v", result)
			default:
			}
			if _, err := system.AskGrain(ctx, hooks, releaseStopping{}, time.Second); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-replies:
				if result.err != nil {
					t.Fatal(result.err)
				}
				_, resumes := result.next.(agentloop.ContinueSteps)
				_, ends := result.next.(agentloop.TurnEnd)
				if resumes == canceled || ends != canceled {
					t.Fatalf("continuation = %T", result.next)
				}
			case <-time.After(time.Second):
				t.Fatal("hook result not delivered")
			}
		})
	}
}

func prepareRequestStep(t *testing.T, subject *agentloop.ReactLoopAgent, history *session.Log) {
	t.Helper()
	commit := func(drafts ...session.EventDraft) session.WriteResult {
		write, err := history.Commit(context.Background(), session.Batch(drafts...))
		if err != nil {
			t.Fatal(err)
		}
		return write
	}
	edit, err := subject.PrepareInput()
	if err != nil {
		t.Fatal(err)
	}
	if err := subject.ConfirmInput(commit(edit.Drafts...)); err != nil {
		t.Fatal(err)
	}
	decision, err := subject.PreparePreStep()
	if err != nil {
		t.Fatal(err)
	}
	if accepted, err := subject.AcceptPreStep(decision.Default); err != nil || !accepted {
		t.Fatalf("admission: %v, %v", accepted, err)
	}
	draft, err := subject.PrepareStep(agentloop.Preparation{
		Snapshot: history.Snapshot(),
	})
	if err != nil || draft == nil {
		t.Fatalf("prepare Step: %v", err)
	}
	if err := subject.ConfirmStep(commit(*draft)); err != nil {
		t.Fatal(err)
	}
	for {
		draft, err := subject.MessageDraft()
		if err != nil {
			t.Fatal(err)
		}
		if draft == nil {
			break
		}
		if err := subject.ConfirmMessage(commit(*draft)); err != nil {
			t.Fatal(err)
		}
	}
	if err := subject.BeginRequest(); err != nil {
		t.Fatal(err)
	}
}

func finishStepForStopping(t *testing.T, subject *agentloop.ReactLoopAgent, history *session.Log) {
	t.Helper()
	prepareRequestStep(t, subject, history)
	ending, err := subject.PrepareStepEnd(stepstate.StepResultCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if err := subject.ConfirmStepEnd(commitStepEnd(t, history, ending)); err != nil {
		t.Fatal(err)
	}
}

func commitStepEnd(t *testing.T, history *session.Log, draft session.EventDraft) session.WriteResult {
	t.Helper()
	write, err := history.Commit(context.Background(), session.Batch(draft))
	if err != nil {
		t.Fatal(err)
	}
	return write
}
