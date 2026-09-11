package promptactor

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"

	"github.com/gorenx/goren/systemprompt"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// ErrBusy means a prior assembly or close still owns its completion.
var ErrBusy = errors.New("systemprompt: operation in progress")

// ErrAsyncRequest rejects callers that cannot receive a deferred business reply.
var ErrAsyncRequest = errors.New("systemprompt: asynchronous request required")

// RuntimeGrain schedules one Agent's assembly independently of shared registration.
type RuntimeGrain struct {
	runtime        *systemprompt.Runtime
	layer          *systemprompt.PromptLayer
	registry       *goakt.GrainIdentity
	version        uint64
	configuration  *goakt.GrainReply
	assembly       *assembly
	closing        *goakt.GrainReply
	closed         bool
	initialization *runtimeInitialization
}

type assemblyFinished struct {
	token  string
	result Assembled
}

type assembly struct {
	token  string
	id     string
	reply  *goakt.GrainReply
	cancel context.CancelFunc
}

// OnActivate acquires no business resources.
func (*RuntimeGrain) OnActivate(context.Context, *goakt.GrainProps) error { return nil }

// OnDeactivate cancels work on forced shutdown; normal CloseRuntime drains first.
func (owner *RuntimeGrain) OnDeactivate(context.Context, *goakt.GrainProps) error {
	if owner.configuration != nil {
		owner.configuration.Response(Completion{
			Err: ErrUnavailable,
		})
		owner.configuration = nil
	}
	if owner.initialization != nil {
		owner.finishInitialization(owner.initialization, ErrUnavailable)
	}
	if owner.assembly != nil {
		owner.assembly.cancel()
		owner.assembly.reply.Response(Assembled{
			ID:  owner.assembly.id,
			Err: ErrUnavailable,
		})
		owner.assembly = nil
	}
	if owner.closing != nil {
		owner.closing.Response(Completion{
			Err: ErrUnavailable,
		})
		owner.closing = nil
	}
	owner.runtime = nil
	owner.layer = nil
	owner.registry = nil
	owner.version = 0
	return nil
}

// OnReceive applies updates in the mailbox and evaluates captured state in a task.
func (owner *RuntimeGrain) OnReceive(gc *goakt.GrainContext) {
	switch incoming := gc.Message().(type) {
	case ReadRuntime:
		if owner.runtime == nil ||
			owner.closed ||
			owner.closing != nil {
			gc.Response(RuntimeView{
				Err: ErrUnavailable,
			})
			return
		}
		gc.Response(RuntimeView{})
	case InitializeRuntime:
		owner.initializeRuntime(gc, incoming)
	case promptPrepared:
		owner.initializeTools(gc, incoming)
	case ApplyTools:
		if owner.runtime == nil ||
			owner.closed ||
			owner.closing != nil {
			gc.Response(Completion{
				Err: ErrUnavailable,
			})
			return
		}
		if owner.configuration != nil {
			gc.Response(Completion{
				Err: ErrBusy,
			})
			return
		}
		gc.Response(Completion{
			Err: owner.runtime.ApplyTools(incoming.AgentID, incoming.Definitions),
		})
	case ApplyConfiguration:
		owner.applyConfiguration(gc, incoming)
	case Assemble:
		owner.startAssembly(gc, incoming)
	case assemblyFinished:
		if owner.assembly != nil && incoming.token == owner.assembly.token {
			owner.assembly.cancel()
			owner.assembly.reply.Response(incoming.result)
			owner.assembly = nil
			if owner.closing != nil {
				owner.runtime = nil
				owner.layer = nil
				owner.registry = nil
				owner.version = 0
				owner.closed = true
				owner.closing.Response(Completion{})
				owner.closing = nil
			}
		}
		gc.Response(Completion{})
	case CancelAssembly:
		if owner.assembly != nil && incoming.ID == owner.assembly.id {
			owner.assembly.cancel()
		}
		gc.Response(Completion{})
	case CloseRuntime:
		if owner.closing != nil || owner.configuration != nil {
			gc.Response(Completion{
				Err: ErrBusy,
			})
			return
		}
		if owner.assembly != nil {
			owner.closing = gc.DeferResponse()
			if owner.closing == nil {
				gc.Response(Completion{
					Err: ErrAsyncRequest,
				})
				return
			}
			owner.assembly.cancel()
			return
		}
		owner.runtime = nil
		owner.layer = nil
		owner.registry = nil
		owner.version = 0
		owner.closed = true
		gc.Response(Completion{})
	default:
		gc.Unhandled()
	}
}

func (owner *RuntimeGrain) startAssembly(gc *goakt.GrainContext, request Assemble) {
	var err error
	switch {
	case owner.runtime == nil ||
		owner.closed ||
		owner.closing != nil:
		err = ErrUnavailable
	case owner.assembly != nil || owner.configuration != nil:
		err = ErrBusy
	case request.ID == "":
		err = errors.New("systemprompt: assembly ID is required")
	}
	if err != nil {
		gc.Response(Assembled{
			ID:  request.ID,
			Err: err,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(Assembled{
			ID:  request.ID,
			Err: ErrAsyncRequest,
		})
		return
	}
	captured := *owner.runtime
	ctx, cancel := context.WithCancel(gc.Context())
	token := uuid.NewString()
	owner.assembly = &assembly{
		token:  token,
		id:     request.ID,
		reply:  reply,
		cancel: cancel,
	}
	err = gc.PipeToSelf(func() (payload any, taskErr error) {
		completed := Assembled{
			ID: request.ID,
		}
		defer func() {
			if failure := recover(); failure != nil {
				payload = assemblyFinished{
					token: token,
					result: Assembled{
						ID:  request.ID,
						Err: fmt.Errorf("systemprompt: assembly panic: %v", failure),
					},
				}
			}
		}()
		completed.Prompt, completed.Err = captured.Assemble(ctx)
		if ctx.Err() != nil {
			completed.Err = ctx.Err()
		}
		return assemblyFinished{
			token:  token,
			result: completed,
		}, nil
	})
	if err != nil {
		cancel()
		owner.assembly.reply.Response(Assembled{
			ID:  request.ID,
			Err: err,
		})
		owner.assembly = nil
	}
}
