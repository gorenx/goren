package loopactor

import (
	"context"
	"errors"
	"github.com/gorenx/goren/llm"
	llmactor "github.com/gorenx/goren/llm/actor"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	"github.com/gorenx/goren/tools"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type executionSetup struct {
	router     *goakt.PID
	handlers   middlewareactor.Handlers
	timeout    time.Duration
	execute    bool
	middleware *goakt.GrainIdentity
	prompt     *goakt.GrainIdentity
	subject    *agentloop.ReactLoopAgent
	target     *goakt.GrainIdentity
}

type executionGrain struct{ Grain }

func (owner *executionGrain) OnReceive(gc *goakt.GrainContext) {
	if input, ok := gc.Message().(executionSetup); ok {
		if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.StashNonReentrant))); err != nil {
			panic(err)
		}
		owner.business = input.subject
		owner.modelRouter = input.router
		owner.handlers = input.handlers.Clone()
		owner.replyTimeout = input.timeout
		owner.conversation = input.target
		owner.tools = input.target
		owner.middleware = input.target
		if input.middleware != nil {
			owner.middleware = input.middleware
		}
		owner.prompt = input.target
		if input.prompt != nil {
			owner.prompt = input.prompt
		}
		owner.creationID = "test"
		if input.execute {
			err := owner.business.Run(owner.services(gc))
			if err != nil {
				panic(err)
			}
		}
		gc.Response(struct{}{})
		return
	}
	owner.Grain.OnReceive(gc)
}

type sessionSetup struct {
	history *session.Log
}

type sessionCounts struct {
	commits int
}

type readCounts struct{}

type sessionGrain struct {
	history *session.Log
	counts  sessionCounts
}

func (*sessionGrain) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*sessionGrain) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *sessionGrain) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case runtimeactor.RefreshConfiguration:
		gc.Response(runtimeactor.Definitions{Value: tools.Definitions{AgentID: owner.history.Header().ID}})
	case sessionSetup:
		owner.history = input.history
		gc.Response(struct{}{})
	case promptactor.ApplyTools:
		gc.Response(promptactor.Completion{})
	case promptactor.Assemble:
		gc.Response(promptactor.Assembled{
			ID: input.ID,
		})
	case middlewareactor.ResolvePreStep:
		gc.Response(agent.PreStepResolved{
			Decision: input.Default,
		})
	case sessionactor.Commit:
		owner.counts.commits++
		write, err := owner.history.Commit(gc.Context(), input.Plan)
		gc.Response(sessionactor.Committed{
			Result: write,
			Err:    err,
		})
	case sessionactor.Read:
		gc.Response(sessionactor.View{
			Snapshot: owner.history.Snapshot(),
		})
	case sessionactor.Flush:
		gc.Response(sessionactor.Flushed{
			Barrier: input.Barrier,
		})
	case readCounts:
		gc.Response(owner.counts)
	default:
		gc.Unhandled()
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
	if err := subject.Restore(history.Header(), history.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := subject.Initialize(); err != nil {
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

type stoppingSetup struct {
	failCancel bool
	phase      string
	entered    chan struct{}
}
type releaseStopping struct{}
type stoppingEndpoint struct {
	invoked    bool
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
	case promptactor.ApplyTools:
		gc.Response(promptactor.Completion{})
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
		if owner.phase != "pre-step" {
			gc.Response(agent.PreStepResolved{Decision: input.Default})
			return
		}
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
		if owner.invoked {
			gc.Response(agent.TurnStoppingCompleted{})
			return
		}
		owner.invoked = true
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

type modelProvider struct {
	calls     atomic.Int32
	failFirst bool
}

func (backend *modelProvider) Stream(context.Context, llm.GenerateOptions) (llm.ChunkStream, error) {
	if backend.calls.Add(1) == 1 && backend.failFirst {
		return nil, llm.MustLlmError("limited", "RATE_LIMITED")
	}
	return llm.NewSliceStream([]llm.StreamChunk{
		llm.BlockStartChunk{
			Index:     0,
			BlockType: "text",
		},
		llm.BlockEndChunk{
			Index: 0,
			Block: agentmessage.NewTextBlock("answer"),
		},
		llm.FinishChunk{
			Reason: llm.StopFinish{},
		},
	})
}

func (*modelProvider) ProviderRetryPolicy(string) (llm.RetryPolicy, error) {
	return llm.NormalRetryPolicy{
		Mode:           llm.RetryNormal,
		MaxRetries:     1,
		RetryableCodes: []string{"RATE_LIMITED"},
		ResolvedRetryBackoff: llm.ResolvedRetryBackoff{
			InitialDelayMS: 1,
			MaxDelayMS:     1,
			JitterRatio:    0,
		},
	}, nil
}

func modelPool(t *testing.T, system goakt.ActorSystem, backend llm.Adapter) *goakt.PID {
	t.Helper()
	ctx := context.Background()
	catalog, err := goakt.GrainOf[*llmactor.Grain](ctx, system, "llm", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	registered, err := system.AskGrain(ctx, catalog, llmactor.RegisterAdapter{
		Providers: []string{"p"},
		Adapter:   backend,
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if registered.(llmactor.Registered).Err != nil {
		t.Fatal(registered)
	}
	configured, err := system.AskGrain(ctx, catalog, llmactor.SetConfiguration{
		AgentID: "settlement",
		Config: llm.CallConfig{
			Provider: "p",
			Model:    "m",
		},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if configured.(llmactor.ConfigurationResult).Err != nil {
		t.Fatal(configured)
	}
	pool, err := llmactor.SpawnPool(ctx, system, "llm-pool", 2, llmactor.InitializeWorker{LLM: catalog})
	if err != nil {
		t.Fatal(err)
	}
	return pool
}
