package toolactor

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	"github.com/gorenx/goren/tools/tool"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Grain orchestrates preparation, catalog registration and resource cleanup.
// The lifecycle owner must close business resources before deactivating it.
type Grain struct {
	instanceID string
	creationID string
	agentID    session.SessionID
	registry   *goakt.GrainIdentity
	authority  *goakt.GrainIdentity
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

type publicationChecked struct {
	operation *operation
	outcome   catalogactor.Outcome
	err       error
}

type completed struct {
	operation *operation
	err       error
}

// OnActivate waits for explicit lifecycle initialization.
func (owner *Grain) OnActivate(context.Context, *goakt.GrainProps) error {
	owner.instanceID = uuid.NewString()
	return nil
}

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
		owner.initialize(gc, input)
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
		owner.checkPublication(gc, current)
	case publicationChecked:
		gc.NoErr()
		if owner.active != input.operation {
			return
		}
		if input.err != nil {
			owner.finish(input.operation, input.err)
			return
		}
		if !input.outcome.Found || input.outcome.Publication.AgentID != owner.agentID {
			owner.finish(input.operation, errors.New("tools: catalog update does not belong to this Agent"))
			return
		}
		request := input.operation.provision
		_, err := owner.business.Schema(request.Revision)
		if err == nil {
			owner.registerDefinition(gc, input.operation)
			return
		}
		if input.outcome.Publication.Published {
			owner.finish(input.operation, errors.New("tools: published update cannot construct a new candidate"))
			return
		}
		business := owner.business
		owner.schedule(gc, input.operation, request.Context, func(ctx context.Context) error {
			return business.Provision(ctx, request.Revision, request.Provider)
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
			owner.registerDefinition(gc, input.operation)
			return
		}
		owner.finish(input.operation, input.err)
	default:
		gc.Unhandled()
	}
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
	err := gc.PipeToSelf(func() (result any, failure error) {
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
	})
	if err != nil {
		owner.finish(current, err)
	}
}

func (owner *Grain) checkPublication(gc *goakt.GrainContext, current *operation) {
	received := make(chan publicationChecked, 1)
	err := gc.PipeToSelf(func() (any, error) { return <-received, nil })
	if err != nil {
		owner.finish(current, err)
		return
	}
	gc.RequestGrain(owner.registry, catalogactor.ReadPublication{
		ID: toolregistry.UpdateID(current.provision.Revision),
	}).Then(func(payload any, failure error) {
		result := publicationChecked{
			operation: current,
			err:       failure,
		}
		if failure == nil {
			outcome, valid := payload.(catalogactor.Outcome)
			if !valid {
				result.err = errors.New("tools: invalid publication reply")
			} else {
				result.outcome = outcome
			}
		}
		received <- result
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
	gc.RequestGrain(
		owner.registry,
		catalogactor.RegisterDefinition{
			ID: toolregistry.UpdateID(string(input.Revision)),
			Candidate: toolregistry.Candidate{
				Schema:  schema,
				Visible: input.Visible,
			},
		},
	).Then(
		func(payload any, failure error) {
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
		},
	)
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
