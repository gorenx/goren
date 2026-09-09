package runtimeactor

import (
	"context"
	"errors"
	"reflect"
	"slices"

	"github.com/google/uuid"
	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/agentmessage"
	toolregistry "github.com/gorenx/goren/tools/registry"
	toolruntime "github.com/gorenx/goren/tools/runtime"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// Runtime schedules one Agent's tool initialization and cleanup. Business state
// and failure recovery belong to toolruntime.Runtime; this actor never starts another actor.
type Runtime struct {
	instanceID string
	// initialization retains the accepted creation binding and borrowed execution policies.
	initialization *InitializeRuntime
	// toolRuntime owns the Agent tool set, update recovery, and resource cleanup.
	toolRuntime *toolruntime.Runtime
	catalog     *Catalog
	instances   *Instances
	active      *operation
	calls       map[agentmessage.CallID]*invocation
	// policyRequest serializes preparation and finalization while body tasks overlap.
	policyRequest *invocation
}

type operation struct {
	reply  *goakt.GrainReply
	cancel context.CancelFunc
}

type authorized struct {
	operation *operation
	err       error
}

type completed struct {
	operation *operation
	err       error
}

// OnActivate acquires no resources and assigns a fresh physical activation identity.
func (owner *Runtime) OnActivate(context.Context, *goakt.GrainProps) error {
	owner.instanceID = uuid.NewString()
	return nil
}

// OnDeactivate cancels work without certifying that external resources were released.
func (owner *Runtime) OnDeactivate(context.Context, *goakt.GrainProps) error {
	for _, current := range owner.calls {
		current.cancel()
		if current.reply != nil {
			current.reply.Response(CallResult{Err: errors.New("tools: runtime stopped before call completion")})
		}
	}
	if owner.active != nil {
		if owner.active.cancel != nil {
			owner.active.cancel()
		}
		owner.finish(owner.active, errors.New("tools: runtime stopped before operation completion"))
	}
	return nil
}

// OnReceive serializes protocol handling; framework tasks exclusively run the business owner.
func (owner *Runtime) OnReceive(gc *goakt.GrainContext) {
	if owner.receiveCalls(gc) {
		return
	}
	switch input := gc.Message().(type) {
	case InitializeRuntime:
		owner.initialize(gc, input)
	case authorized:
		gc.NoErr()
		if owner.active != input.operation {
			return
		}
		if input.err != nil {
			owner.finish(input.operation, input.err)
			return
		}
		settings := owner.initialization
		business, err := toolruntime.NewRuntime(settings.AgentID)
		if err != nil {
			owner.finish(input.operation, err)
			return
		}
		directory, err := NewCatalog(gc.ActorSystem(), settings.Registry, settings.Timeout)
		if err != nil {
			owner.finish(input.operation, err)
			return
		}
		instances, err := NewInstances(
			gc.ActorSystem(),
			settings.Authority,
			settings.Registry,
			settings.AgentID,
			settings.CreationID,
			settings.Timeout,
		)
		if err != nil {
			owner.finish(input.operation, err)
			return
		}
		owner.toolRuntime = business
		owner.catalog = directory
		owner.instances = instances
		identifier := toolregistry.UpdateID(uuid.NewString())
		owner.schedule(gc, input.operation, func(ctx context.Context) error {
			return business.Initialize(ctx, identifier, settings.Overrides, directory, instances)
		})
	case completed:
		gc.NoErr()
		owner.finish(input.operation, input.err)
	case ReadRuntime:
		result := RuntimeView{}
		if owner.initialization == nil ||
			owner.initialization.CreationID != input.CreationID ||
			owner.toolRuntime == nil ||
			owner.active != nil {
			result.Err = errors.New("tools: runtime snapshot is unavailable")
		} else {
			result.Snapshot, result.Err = owner.toolRuntime.Snapshot()
		}
		gc.Response(result)
	case CloseRuntime:
		if len(owner.calls) != 0 {
			gc.Response(Completion{Err: errors.New("tools: executions must settle and release before closing")})
			return
		}
		if input.InstanceID != owner.instanceID ||
			owner.initialization == nil ||
			owner.initialization.CreationID != input.CreationID {
			gc.Response(Completion{Err: errors.New("tools: close does not own this runtime activation")})
			return
		}
		if owner.active != nil {
			gc.Response(Completion{Err: errors.New("tools: runtime operation is still running")})
			return
		}
		if owner.toolRuntime == nil {
			gc.Response(Completion{})
			return
		}
		current := owner.deferOperation(gc)
		if current == nil {
			return
		}
		business := owner.toolRuntime
		directory := owner.catalog
		instances := owner.instances
		owner.schedule(gc, current, func(ctx context.Context) error { return business.Close(ctx, directory, instances) })
	default:
		gc.Unhandled()
	}
}

func (owner *Runtime) initialize(gc *goakt.GrainContext, input InitializeRuntime) {
	if input.AgentID == "" ||
		input.CreationID == "" ||
		input.Authority == nil ||
		input.Registry == nil ||
		input.Timeout <= 0 {
		gc.Response(Completion{Err: errors.New("tools: runtime creation and owner routes are required")})
		return
	}
	if owner.active != nil {
		gc.Response(Completion{Err: errors.New("tools: runtime operation is still running")})
		return
	}
	if owner.initialization != nil {
		var err error
		if !reflect.DeepEqual(*owner.initialization, input) {
			err = errors.New("tools: runtime is bound to a different initialization")
		} else if owner.toolRuntime == nil {
			err = errors.New("tools: runtime initialization failed")
		} else {
			_, err = owner.toolRuntime.Snapshot()
		}
		gc.Response(Completion{Err: err})
		return
	}
	// Automatic reactivation may not retain the original GrainOf options.
	// Enable requests before verifying this activation with the lifecycle owner.
	err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll)))
	if err != nil {
		gc.Response(Completion{Err: err})
		return
	}
	current := owner.deferOperation(gc)
	if current == nil {
		return
	}
	input.Overrides = slices.Clone(input.Overrides)
	input.Policies.Guards = slices.Clone(input.Policies.Guards)
	owner.initialization = &input
	received := make(chan authorized, 1)
	err = gc.PipeToSelf(func() (any, error) { return <-received, nil })
	if err != nil {
		owner.finish(current, err)
		return
	}
	gc.RequestGrain(
		input.Authority, agentactor.ClaimTools{
			AgentID:    input.AgentID,
			CreationID: input.CreationID,
			InstanceID: owner.instanceID,
		},
	).Then(
		func(payload any, failure error) {
			if failure == nil {
				claimed, valid := payload.(agentactor.Claimed)
				if !valid {
					failure = errors.New("tools: invalid runtime authorization reply")
				} else {
					failure = claimed.Err
				}
			}
			received <- authorized{
				operation: current,
				err:       failure,
			}
		},
	)
}

func (owner *Runtime) deferOperation(gc *goakt.GrainContext) *operation {
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(Completion{Err: errors.New("tools: deferred runtime response required")})
		return nil
	}
	current := &operation{reply: reply}
	owner.active = current
	return current
}

func (owner *Runtime) schedule(gc *goakt.GrainContext, current *operation, run func(context.Context) error) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	current.cancel = cancel
	err := gc.PipeToSelf(func() (result any, failure error) {
		defer func() {
			if recover() != nil {
				result = completed{
					operation: current,
					err:       errors.New("tools: runtime operation panicked"),
				}
			}
		}()
		return completed{
			operation: current,
			err:       run(ctx),
		}, nil
	})
	if err != nil {
		owner.finish(current, err)
	}
}

func (owner *Runtime) finish(current *operation, failure error) {
	if owner.active != current {
		return
	}
	if current.cancel != nil {
		current.cancel()
	}
	owner.active = nil
	current.reply.Response(Completion{Err: failure})
}
