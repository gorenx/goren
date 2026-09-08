package toolactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	"github.com/gorenx/goren/tools/tool"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// Grain orchestrates preparation, catalog registration and resource cleanup.
// The lifecycle owner must close business resources before deactivating it.
type Grain struct {
	creationID string
	agentID    session.SessionID
	registry   *goakt.GrainIdentity
	business   *tool.Tool
	active     *operation
	// calls retains delivery and cancellation only until each body has returned.
	calls map[agentmessage.CallID]*call
}

// operation retains delivery state while one framework task owns the business object.
type operation struct {
	reply     *goakt.GrainReply
	cancel    context.CancelFunc
	provision *Provision
}

type completed struct {
	operation *operation
	err       error
}

// OnActivate waits for explicit lifecycle initialization.
func (*Grain) OnActivate(context.Context, *goakt.GrainProps) error { return nil }

// OnDeactivate cancels pending construction but does not certify resource closure.
func (owner *Grain) OnDeactivate(context.Context, *goakt.GrainProps) error {
	for identifier, current := range owner.calls {
		current.cancel()
		current.reply.Err(errors.New("tools: Grain stopped during execution; body completion is unconfirmed"))
		delete(owner.calls, identifier)
	}
	if owner.active != nil {
		if owner.active.cancel != nil {
			owner.active.cancel()
		}
		owner.active.reply.Response(Completion{
			Err: errors.New("tools: Grain stopped before operation completed"),
		})
		owner.active = nil
	}
	return nil
}

// OnReceive handles framework scheduling; resource decisions stay in Tool.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	if owner.receiveExecution(gc) {
		return
	}
	switch input := gc.Message().(type) {
	case Initialize:
		gc.Response(Completion{
			Err: owner.initialize(gc, input),
		})
	case Provision:
		if !owner.admit(gc, input.CreationID) {
			return
		}
		if input.Context != nil && input.Context.Err() != nil {
			gc.Response(Completion{
				Err: context.Cause(input.Context),
			})
			return
		}
		current := owner.deferOperation(gc)
		if current == nil {
			return
		}
		current.provision = &input
		if _, err := owner.business.Schema(input.Revision); err == nil {
			owner.register(gc, current)
			return
		}
		business := owner.business
		owner.schedule(gc, current, input.Context, func(ctx context.Context) error {
			return business.Provision(ctx, input.Revision, input.Provider)
		})
	case Commit:
		if owner.admit(gc, input.CreationID) {
			gc.Response(Completion{
				Err: owner.business.Commit(input.Revision),
			})
		}
	case Retire:
		if owner.admit(gc, input.CreationID) {
			business := owner.business
			owner.schedule(gc, owner.deferOperation(gc), input.Context, func(ctx context.Context) error {
				return business.Retire(ctx, input.Revision)
			})
		}
	case Abort:
		if owner.admit(gc, input.CreationID) {
			business := owner.business
			owner.schedule(gc, owner.deferOperation(gc), input.Context, func(ctx context.Context) error {
				return business.Abort(ctx, input.Revision)
			})
		}
	case Close:
		if owner.admit(gc, input.CreationID) {
			business := owner.business
			owner.schedule(gc, owner.deferOperation(gc), input.Context, business.Close)
		}
	case completed:
		gc.NoErr()
		if owner.active != input.operation {
			return
		}
		if input.err == nil && input.operation.provision != nil {
			owner.register(gc, input.operation)
			return
		}
		owner.finish(input.operation, input.err)
	default:
		gc.Unhandled()
	}
}

func (owner *Grain) initialize(gc *goakt.GrainContext, input Initialize) error {
	if owner.business != nil {
		return errors.New("tools: Grain is already initialized")
	}
	if input.CreationID == "" || input.AgentID == "" || input.Registry == nil {
		return errors.New("tools: creation, Agent and catalog identities are required")
	}
	business, err := tool.New(input.Name)
	if err != nil {
		return err
	}
	if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))); err != nil {
		return err
	}
	owner.business = business
	owner.creationID = input.CreationID
	owner.agentID = input.AgentID
	owner.registry = input.Registry
	return nil
}

func (owner *Grain) admit(gc *goakt.GrainContext, creationID string) bool {
	var err error
	if owner.business == nil || creationID != owner.creationID {
		err = errors.New("tools: request does not own this tool creation")
	} else if owner.active != nil || len(owner.calls) != 0 {
		err = errors.New("tools: tool operation is still running")
	}
	if err != nil {
		gc.Response(Completion{
			Err: err,
		})
		return false
	}
	return true
}

func (owner *Grain) deferOperation(gc *goakt.GrainContext) *operation {
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(Completion{
			Err: errors.New("tools: operation requires an asynchronous request"),
		})
		return nil
	}
	current := &operation{
		reply: reply,
	}
	owner.active = current
	return current
}

func (owner *Grain) schedule(
	gc *goakt.GrainContext,
	current *operation,
	ctx context.Context,
	run func(context.Context) error,
) {
	if current == nil {
		return
	}
	if ctx == nil {
		ctx = context.WithoutCancel(gc.Context())
	}
	work, cancel := context.WithCancel(ctx)
	current.cancel = cancel
	if err := gc.PipeToSelf(func() (result any, failure error) {
		defer func() {
			if recover() != nil {
				result = completed{
					operation: current,
					err:       errors.New("tools: resource operation panicked"),
				}
			}
		}()
		return completed{
			operation: current,
			err:       run(work),
		}, nil
	}); err != nil {
		owner.finish(current, err)
	}
}

func (owner *Grain) register(gc *goakt.GrainContext, current *operation) {
	gc.RequestGrain(owner.registry, catalogactor.Lookup{
		ID: toolregistry.UpdateID(current.provision.Revision.ID),
	}).Then(func(payload any, failure error) {
		if owner.active != current {
			return
		}
		if failure != nil {
			owner.finish(current, failure)
			return
		}
		outcome, ok := payload.(catalogactor.Outcome)
		if !ok || !outcome.Found || outcome.Publication.AgentID != owner.agentID {
			owner.finish(current, errors.New("tools: catalog update does not belong to this Agent"))
			return
		}
		owner.registerDefinition(gc, current)
	})
}

func (owner *Grain) registerDefinition(gc *goakt.GrainContext, current *operation) {
	input := current.provision
	if input.Context != nil && input.Context.Err() != nil {
		owner.finish(current, context.Cause(input.Context))
		return
	}
	schema, err := owner.business.Schema(input.Revision)
	if err != nil {
		owner.finish(current, err)
		return
	}
	gc.RequestGrain(owner.registry, catalogactor.Register{
		ID: toolregistry.UpdateID(input.Revision.ID),
		Candidate: toolregistry.Candidate{
			Schema:  schema,
			Visible: input.Visible,
		},
	}).Then(func(payload any, failure error) {
		if owner.active != current {
			return
		}
		if failure == nil {
			response, ok := payload.(catalogactor.Completion)
			if !ok {
				failure = errors.New("tools: invalid catalog registration reply")
			} else {
				failure = response.Err
			}
		}
		owner.finish(current, failure)
	})
}

func (owner *Grain) finish(current *operation, err error) {
	if current.cancel != nil {
		current.cancel()
	}
	owner.active = nil
	current.reply.Response(Completion{
		Err: err,
	})
}
