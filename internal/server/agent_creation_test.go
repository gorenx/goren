package server

import (
	"context"
	"errors"
	"github.com/gorenx/goren/toolaskuser"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	"github.com/gorenx/goren/tools/tool"
	"github.com/gorenx/goren/userquestions"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	llmactor "github.com/gorenx/goren/llm/actor"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence/sqlite"
	sessionregistry "github.com/gorenx/goren/session/registry"
	"github.com/gorenx/goren/systemprompt"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// This acceptance test uses production configuration owners. A fabricated tool
// baseline would hide a missing initialization stage in Agent lifecycle wiring.
func TestAgentCreationWithProductionConfigurationOwners(t *testing.T) {
	for _, name := range []string{"empty", "tool", "failed-tool", "cancelled-tool"} {
		withTool := name != "empty"
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			storage, err := sqlite.Open(ctx, sqlite.Config{
				Path:        filepath.Join(t.TempDir(), "sessions.sqlite"),
				JournalMode: sqlite.JournalWAL,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer storage.Close(context.Background())
			system, err := goakt.NewActorSystem("server-agent-integration", goakt.WithPubSub(), goakt.WithLogger(log.DiscardLogger))
			if err != nil {
				t.Fatal(err)
			}
			if err := system.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer system.Stop(context.Background())
			if _, err := initializeModels(ctx, system, DefaultModelConfig()); err != nil {
				t.Fatal(err)
			}
			if _, err := initializePrompts(ctx, system, systemprompt.Config{}); err != nil {
				t.Fatal(err)
			}
			models, err := goakt.GrainOf[*llmactor.Grain](ctx, system, llmactor.Name, goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			prompts, err := goakt.GrainOf[*promptactor.Grain](ctx, system, promptactor.Name, goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			source := &questionProvider{}
			if name == "cancelled-tool" {
				source.wait = &questionWait{
					entered:   make(chan struct{}, 1),
					cancelled: make(chan struct{}, 1),
					release:   make(chan struct{}),
				}
			}
			source.failure.Store(name == "failed-tool")
			var defaults []toolregistry.Tool
			if withTool {
				defaults = []toolregistry.Tool{{
					Name:     toolaskuser.Name,
					Provider: source,
					Visible:  true,
				}}
			}
			directory, err := initializeTools(ctx, system, defaults)
			if err != nil {
				t.Fatal(err)
			}
			sessions, err := goakt.GrainOf[*sessionregistry.RegistryGrain](ctx, system, sessionregistry.RegistryName, goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			authority, err := goakt.GrainOf[*agentregistry.Grain](ctx, system, agentregistry.Name,
				goakt.WithLongLivedGrain(),
				goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				if err := closeAgents(cleanup, system, authority); err != nil {
					t.Error(err)
				}
			}()
			input := agentregistry.CreateAgent{
				Creation: agentregistry.Creation{
					AgentID:    "conversation",
					CreationID: "current",
				},
				Options:         session.CreateOptions{},
				Storage:         storage,
				SessionRegistry: sessions,
				ModelConfig:     models,
				PromptRegistry:  prompts,
				ToolsRegistry:   directory,
				Timeout:         time.Second,
			}
			payload, err := system.AskGrain(ctx, authority, input, 4*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			ready, ok := payload.(agentregistry.Ready)
			if !ok {
				t.Fatalf("invalid creation response: %T", payload)
			}
			if name == "failed-tool" {
				if ready.Err == nil || source.opens.Load() != 1 || source.closes.Load() != 1 {
					t.Fatalf("failed construction did not clean resource: %+v, opens %d closes %d", ready, source.opens.Load(), source.closes.Load())
				}
				// The creation error starts automatic cleanup; it does not certify completion.
				// Join that close attempt before requesting a new lifetime.
				payload, err = system.AskGrain(ctx, authority, agentregistry.CloseAgent{
					Creation: input.Creation,
				}, 4*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				closed, valid := payload.(agentactor.Closed)
				if !valid || closed.Err != nil {
					t.Fatalf("failed creation cleanup: %v", payload)
				}
				source.failure.Store(false)
				input.Creation.CreationID = "recovered"
				payload, err = system.AskGrain(ctx, authority, input, 4*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				ready, ok = payload.(agentregistry.Ready)
				if !ok {
					t.Fatalf("invalid recovery response: %T", payload)
				}
			}
			if ready.Err != nil {
				t.Fatalf("production Agent creation failed: %v", ready.Err)
			}
			if ready.Executor == nil {
				t.Fatal("ready Agent has no execution route")
			}

			target, err := goakt.GrainOf[*runtimeactor.Grain](ctx, system, runtimeactor.RuntimeName("conversation"), goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			payload, err = system.AskGrain(ctx, target, runtimeactor.ReadRuntime{
				CreationID: input.Creation.CreationID,
			}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, valid := payload.(runtimeactor.RuntimeView)
			if !valid || snapshot.Err != nil {
				t.Fatalf("runtime snapshot: %v", payload)
			}
			var names []string
			var opens int32
			if withTool {
				names = []string{toolaskuser.Name}
				opens = 1
			}
			if name == "failed-tool" {
				opens++
			}
			if !slices.Equal(snapshot.Snapshot.Names, names) || source.opens.Load() != opens {
				t.Fatalf("runtime membership %v, provider opens %d", snapshot.Snapshot.Names, source.opens.Load())
			}
			if withTool {
				if source.wait != nil {
					defer source.wait.finish()
				}
				prepared := runtimeRequest[runtimeactor.CallResult](t, ctx, system, target, runtimeactor.PrepareCall{
					CreationID: input.Creation.CreationID,
					Input: tools.ToolExecutionInput{
						CallID:    "question",
						Name:      toolaskuser.Name,
						SessionID: input.Creation.AgentID,
						Arguments: []byte(`{"questions":[{"id":"q","question":"Continue?"}]}`),
					},
					Context: ctx,
				})
				if prepared.Err != nil || prepared.Stage != tools.ScheduledDispatch {
					t.Fatalf("preparation: %+v", prepared)
				}
				premature := runtimeRequest[runtimeactor.Completion](t, ctx, system, target, runtimeactor.ReleaseCall{
					CreationID: input.Creation.CreationID,
					CallID:     "question",
				})
				if premature.Err == nil {
					t.Fatal("unexecuted call released")
				}
				final := runtimeRequest[runtimeactor.CallResult](t, ctx, system, target, runtimeactor.FinalizeCall{
					CreationID: input.Creation.CreationID,
					CallID:     "question",
				})
				if final.Err == nil {
					t.Fatal("unexecuted call finalized")
				}
				dispatchInput := runtimeactor.DispatchCall{
					CreationID: input.Creation.CreationID,
					CallID:     "question",
				}
				if source.wait == nil {
					dispatched := runtimeRequest[runtimeactor.CallResult](t, ctx, system, target, dispatchInput)
					if dispatched.Err != nil {
						t.Fatal(dispatched.Err)
					}
				} else {
					type dispatchReply struct {
						payload any
						err     error
					}
					received := make(chan dispatchReply, 1)
					go func() {
						payload, failure := system.AskGrain(ctx, target, dispatchInput, 4*time.Second)
						received <- dispatchReply{
							payload: payload,
							err:     failure,
						}
					}()
					select {
					case <-source.wait.entered:
					case <-ctx.Done():
						t.Fatal("tool body did not start")
					}
					cancellation := runtimeRequest[runtimeactor.Completion](t, ctx, system, target, runtimeactor.CancelCall{
						CreationID: input.Creation.CreationID,
						CallID:     "question",
					})
					if cancellation.Err != nil {
						t.Fatal(cancellation.Err)
					}
					select {
					case <-source.wait.cancelled:
					case <-ctx.Done():
						t.Fatal("tool body did not receive cancellation")
					}
					select {
					case <-received:
						t.Fatal("cancellation reply fabricated body completion")
					default:
					}
					released := runtimeRequest[runtimeactor.Completion](t, ctx, system, target, runtimeactor.ReleaseCall{
						CreationID: input.Creation.CreationID,
						CallID:     "question",
					})
					if released.Err == nil || source.closes.Load() != 0 {
						t.Fatal("cancelled but running body released resources")
					}
					source.wait.finish()
					select {
					case reply := <-received:
						result, valid := reply.payload.(runtimeactor.CallResult)
						if reply.err != nil || !valid || result.Err != nil {
							t.Fatalf("body exit: %+v", reply)
						}
					case <-ctx.Done():
						t.Fatal("tool body exit not confirmed")
					}
				}
				final = runtimeRequest[runtimeactor.CallResult](t, ctx, system, target, runtimeactor.FinalizeCall{
					CreationID: input.Creation.CreationID,
					CallID:     "question",
				})
				if final.Err != nil || final.Result == nil || final.Result.Failed() != (source.wait != nil) {
					t.Fatalf("final result: %+v", final)
				}
				released := runtimeRequest[runtimeactor.Completion](t, ctx, system, target, runtimeactor.ReleaseCall{
					CreationID: input.Creation.CreationID,
					CallID:     "question",
				})
				if released.Err != nil {
					t.Fatal(released.Err)
				}
			}
			err = closeAgents(ctx, system, authority)
			if err != nil {
				t.Fatal(err)
			}
			if source.closes.Load() != opens {
				t.Fatalf("provider closes %d, want %d", source.closes.Load(), opens)
			}
		})
	}
}

type questionProvider struct {
	wait    *questionWait
	failure atomic.Bool
	opens   atomic.Int32
	closes  atomic.Int32
}

type questionResource struct {
	closes *atomic.Int32
}

func (owned *questionResource) Close(context.Context) error {
	owned.closes.Add(1)
	return nil
}

func (source *questionProvider) Open(context.Context) (tool.Implementation, error) {
	source.opens.Add(1)
	definition, err := toolaskuser.NewDefinition(questionService{wait: source.wait})
	if source.failure.Load() {
		err = errors.New("provider opening failed after acquiring resource")
	}
	return tool.Implementation{
		Definition: definition,
		Resource: &questionResource{
			closes: &source.closes,
		},
	}, err
}

type questionService struct {
	wait *questionWait
}

func (service questionService) Ask(ctx context.Context, _ userquestions.Request) (userquestions.Answer, error) {
	if service.wait != nil {
		service.wait.entered <- struct{}{}
		select {
		case <-ctx.Done():
			service.wait.cancelled <- struct{}{}
		case <-service.wait.release:
		}
		<-service.wait.release
		return userquestions.Answer{}, context.Cause(ctx)
	}
	return userquestions.Answer{
		Answers: []userquestions.AnswerItem{},
	}, nil
}

func runtimeRequest[T any](t *testing.T, ctx context.Context, system goakt.ActorSystem, target *goakt.GrainIdentity, input any) T {
	t.Helper()
	payload, err := system.AskGrain(ctx, target, input, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, valid := payload.(T)
	if !valid {
		t.Fatalf("invalid runtime reply %T", payload)
	}
	return result
}

type questionWait struct {
	entered   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (pending *questionWait) finish() {
	pending.once.Do(func() { close(pending.release) })
}
