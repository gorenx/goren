package loopactor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm/modelconfig"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type settlementSetup struct {
	prompt  *goakt.GrainIdentity
	subject *agentloop.ReactLoopAgent
	target  *goakt.GrainIdentity
}

type settlementAgent struct{ Grain }

func (owner *settlementAgent) OnReceive(gc *goakt.GrainContext) {
	if input, ok := gc.Message().(settlementSetup); ok {
		if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.StashNonReentrant))); err != nil {
			panic(err)
		}
		owner.business = input.subject
		owner.conversation = input.target
		owner.prompt = input.target
		if input.prompt != nil {
			owner.prompt = input.prompt
		}
		owner.creationID = "test"
		gc.Response(struct{}{})
		return
	}
	owner.Grain.OnReceive(gc)
}

type storageSetup struct {
	history *session.Log
	failure string
}

type storageCounts struct {
	prompts    int
	admissions int
	commits    int
	reads      int
	flushes    int
}

type readCounts struct{}

type settlementStorage struct {
	history *session.Log
	failure string
	counts  storageCounts
}

func (*settlementStorage) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*settlementStorage) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *settlementStorage) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case storageSetup:
		owner.history = input.history
		owner.failure = input.failure
		gc.Response(struct{}{})
	case promptactor.Assemble:
		owner.counts.prompts++
		gc.Response(promptactor.Assembled{
			ID: input.ID,
		})
	case middlewareactor.ResolvePreStep:
		owner.counts.admissions++
		gc.Response(agent.PreStepResolved{
			Decision: input.Default,
		})
	case sessionactor.Commit:
		owner.counts.commits++
		if owner.failure == "claim rejected" || owner.failure == "turn rejected" {
			owner.failure = ""
			gc.Response(sessionactor.Committed{
				Err: errors.New("test: claim rejected before append"),
			})
			return
		}
		write, err := owner.history.Commit(gc.Context(), input.Plan)
		if owner.failure == "claim reply" && len(write.Events) > 0 && write.Events[0].Type == agent.InboxSplicedEventName {
			owner.failure = ""
			err = errors.New("test: claim reply lost")
		}
		if owner.failure == "step reply" && len(write.Events) == 1 && write.Events[0].Type == session.StepStartEventName {
			owner.failure = ""
			err = errors.New("test: Step start reply lost")
		}
		if owner.failure == "turn reply" {
			owner.failure = ""
			err = errors.New("test: Turn start reply lost")
		}
		if owner.failure == "commit reply" {
			owner.failure = ""
			err = errors.New("test: commit reply lost")
		}
		gc.Response(sessionactor.Committed{
			Result: write,
			Err:    err,
		})
	case sessionactor.Read:
		owner.counts.reads++
		gc.Response(sessionactor.View{
			Snapshot: owner.history.Snapshot(),
		})
	case sessionactor.Flush:
		owner.counts.flushes++
		var err error
		if owner.failure == "flush" {
			owner.failure = ""
			err = errors.New("test: flush failed")
		}
		gc.Response(sessionactor.Flushed{
			Barrier: input.Barrier,
			Err:     err,
		})
	case readCounts:
		gc.Response(owner.counts)
	default:
		gc.Unhandled()
	}
}

type closureRequest struct {
	target *goakt.GrainIdentity
}

type closureCaller struct {
	replies chan error
}

func (*closureCaller) PreStart(*goakt.Context) error { return nil }
func (*closureCaller) PostStop(*goakt.Context) error { return nil }
func (owner *closureCaller) Receive(rc *goakt.ReceiveContext) {
	if input, ok := rc.Message().(closureRequest); ok {
		replies := owner.replies
		rc.RequestGrain(input.target, finishTurn{
			problem: errors.New("test: execution failed"),
		}).Then(func(payload any, failure error) {
			if failure == nil {
				result, ok := payload.(turnFinished)
				if !ok {
					failure = errors.New("test: unexpected closure response")
				} else {
					failure = result.err
				}
			}
			replies <- failure
		})
	}
}

func TestActorSettlementRequestsOnlyRequiredOperations(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		failure  string
		expected storageCounts
	}{
		{
			name: "normal",
			expected: storageCounts{
				commits: 1,
				flushes: 1,
			},
		},
		{
			name:    "lost commit reply",
			failure: "commit reply",
			expected: storageCounts{
				commits: 2,
				reads:   0,
				flushes: 1,
			},
		},
		{
			name:    "failed flush",
			failure: "flush",
			expected: storageCounts{
				commits: 1,
				flushes: 2,
			},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			system, err := goakt.NewActorSystem("settlement", goakt.WithLogger(log.DiscardLogger))
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
			storage, err := goakt.GrainOf[*settlementStorage](ctx, system, "storage", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			// Fixture setup transfers ownership before asynchronous execution begins.
			if _, err := system.AskGrain(ctx, storage, storageSetup{
				history: history,
				failure: scenario.failure,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			identity, err := goakt.GrainOf[*settlementAgent](ctx, system, "execution", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := system.AskGrain(ctx, identity, settlementSetup{
				subject: subject,
				target:  storage,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			replies := make(chan error, 1)
			driver, err := system.Spawn(ctx, "caller", &closureCaller{
				replies: replies,
			}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
			if err != nil {
				t.Fatal(err)
			}
			attempts := 1
			if scenario.failure != "" {
				attempts++
			}
			for index := 0; index < attempts; index++ {
				if err := goakt.Tell(ctx, driver, closureRequest{
					target: identity,
				}); err != nil {
					t.Fatal(err)
				}
				select {
				case failure := <-replies:
					expectedFailure := index == 0 && scenario.failure != ""
					if (failure != nil) != expectedFailure {
						t.Fatalf("attempt %d: %v", index, failure)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("closure timed out")
				}
			}
			payload, err := system.AskGrain(ctx, storage, readCounts{}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if actual := payload.(storageCounts); actual != scenario.expected {
				t.Fatalf("operations = %+v; want %+v", actual, scenario.expected)
			}
		})
	}
}

func acceptedTurn(t *testing.T) (*agentloop.ReactLoopAgent, *session.Log) {
	t.Helper()
	ctx := context.Background()
	history, err := session.NewLog("settlement", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	subject := &agentloop.ReactLoopAgent{}
	if _, err := subject.Restore(history.Header(), history.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := subject.Initialize(modelconfig.Resolved{}, 1); err != nil {
		t.Fatal(err)
	}
	prompt, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("turn")},
		Source:  agentmessage.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := subject.Followup(prompt)
	if err != nil {
		t.Fatal(err)
	}
	write, err := history.Commit(ctx, session.Batch(admission.Edit.Drafts...))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := subject.ConfirmAdmission(admission, write); err != nil {
		t.Fatal(err)
	}
	return subject, history
}

func preparedTurn(t *testing.T) (*agentloop.ReactLoopAgent, *session.Log) {
	t.Helper()
	subject, history := acceptedTurn(t)
	ctx := context.Background()
	draft, err := subject.PrepareTurn()
	if err != nil {
		t.Fatal(err)
	}
	write, err := history.Commit(ctx, turnStartWrite{
		event: draft,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := subject.ConfirmTurn(write); err != nil {
		t.Fatal(err)
	}

	return subject, history
}
