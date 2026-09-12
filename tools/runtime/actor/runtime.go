package runtimeactor

import (
	"context"
	"errors"

	"github.com/google/uuid"
	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/mcpclient"
	"github.com/gorenx/goren/session"
	toolruntime "github.com/gorenx/goren/tools/runtime"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// Runtime schedules one Agent's tool initialization and cleanup. Business state
// and failure recovery belong to toolruntime.Runtime; this actor never starts another actor.
type Runtime struct {
	// tools maps tool names to this Runtime's exact activation and cleanup responsibility.
	// Failed initialization or close retains the entry; successful removal deletes it.
	tools      map[string]*toolLifecycle
	instanceID string
	// These identities bind requests to the initialized Agent lifetime.
	creationID string
	agentID    session.SessionID
	// toolRuntime owns the Agent tool set, update recovery, and resource cleanup.
	toolRuntime *toolruntime.Runtime
	catalog     *Catalog
	instances   *Instances
	// connections owns Session MCP transports; tools borrow them until release.
	connections *connections
	active      *operation
	// calls maps a model tool-call ID to its retained business Call and current
	// cancellation/reply handles. Entries remain until result consumption releases
	// them; a cancellation request alone does not remove the execution obligation.
	calls map[agentmessage.CallID]*invocation
	// policyRequest serializes preparation and finalization while body tasks overlap.
	policyRequest *invocation
	// changed remembers configuration notifications until the next Turn boundary.
	changed bool
}

type operation struct {
	initialization *InitializeRuntime
	refreshID      string
	reply          *goakt.GrainReply
	cancel         context.CancelFunc
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
	for _, instance := range owner.tools {
		if instance.active != nil {
			pending := instance.active
			pending.cancel()
			failure := errors.New("tools: Runtime stopped before tool lifecycle completion")
			if pending.remove {
				pending.reply.Response(Completion{Err: failure})
			} else {
				pending.reply.Response(toolInstance{Err: failure})
			}
		}
	}
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
	if owner.receiveTools(gc) || owner.receiveCalls(gc) || owner.receiveConfiguration(gc) {
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
		settings := input.operation.initialization
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
			gc.Self(),
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
		peers := &connections{
			servers: make(map[string]*mcpclient.Connection),
		}
		if source, ok := gc.ActorSystem().Extension(providerID).(*Provider); ok {
			peers.questions = source.questions
		}
		owner.connections = peers
		directory.connections = peers
		// The first Turn rechecks facts changed before subscription became active.
		owner.changed = true
		owner.schedule(
			gc,
			input.operation,
			func(ctx context.Context) error {
				if settings.Config != nil {
					if err := directory.ConfigureSession(ctx, settings.AgentID, *settings.Config); err != nil {
						return err
					}
				}
				configuration, err := directory.configuration(ctx, settings.AgentID)
				if err != nil {
					return err
				}
				definitions := append(configuration.Global.MCP, configuration.Custom.MCP...)
				if err := peers.reconcile(ctx, definitions); err != nil {
					return err
				}
				return business.Initialize(ctx, directory, instances)
			},
		)
	case completed:
		gc.NoErr()
		if input.operation.refreshID != "" {
			owner.finishRefresh(gc, input.operation, input.err)
			return
		}
		owner.finish(input.operation, input.err)
	case ReadRuntime:
		result := RuntimeView{}
		if owner.creationID == "" ||
			owner.creationID != input.CreationID ||
			owner.toolRuntime == nil ||
			owner.active != nil {
			result.Err = errors.New("tools: runtime snapshot is unavailable")
		} else {
			result.Snapshot, result.Err = owner.toolRuntime.Snapshot()
		}
		gc.Response(result)
	case ReadDefinitions:
		result := Definitions{}
		if owner.creationID != input.CreationID || owner.toolRuntime == nil || owner.active != nil {
			result.Err = errors.New("tools: definitions do not belong to an available runtime")
		} else {
			result.Value, result.Err = owner.toolRuntime.Definitions()
		}
		gc.Response(result)
	case CloseRuntime:
		if len(owner.calls) != 0 {
			gc.Response(Completion{Err: errors.New("tools: executions must settle and release before closing")})
			return
		}
		if input.InstanceID != owner.instanceID ||
			owner.creationID == "" ||
			owner.creationID != input.CreationID {
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
		instances := owner.instances
		peers := owner.connections
		owner.schedule(gc, current, func(ctx context.Context) error {
			if err := business.Close(ctx, instances); err != nil {
				return err
			}
			return peers.close(ctx)
		})
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
	if owner.creationID != "" {
		var err error
		if owner.creationID != input.CreationID || owner.agentID != input.AgentID {
			err = errors.New("tools: runtime is bound to a different initialization")
		} else if owner.toolRuntime == nil {
			err = errors.New("tools: runtime initialization failed")
		} else {
			_, err = owner.toolRuntime.Snapshot()
		}
		gc.Response(Completion{Err: err})
		return
	}
	// Enable outgoing requests for this activation before claiming its lifetime.
	// The caller supplies the incoming async envelope independently of activation options.
	err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll)))
	if err != nil {
		gc.Response(Completion{Err: err})
		return
	}
	current := owner.deferOperation(gc)
	if current == nil {
		return
	}
	if input.Config != nil {
		captured := input.Config.Clone()
		input.Config = &captured
	}
	current.initialization = &input
	owner.creationID = input.CreationID
	owner.agentID = input.AgentID
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
	if current.refreshID != "" {
		owner.changed = true
		current.reply.Response(Definitions{Err: failure})
		return
	}
	current.reply.Response(Completion{Err: failure})
}
