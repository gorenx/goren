package runtimeactor

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/mcpclient"
	"github.com/gorenx/goren/session"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	toolruntime "github.com/gorenx/goren/tools/runtime"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// Runtime schedules one Agent's tool initialization and cleanup. Business state
// and failure recovery belong to toolruntime.Runtime; this actor never starts another actor.
type Runtime struct {
	// actors maps tool names to this Runtime's exact activation and cleanup responsibility.
	// Failed initialization or close retains the entry; successful removal deletes it.
	actors     map[string]*toolLifecycle
	instanceID string
	// These identities bind requests to the initialized Agent lifetime.
	creationID string
	agentID    session.SessionID
	// collection owns applied tool membership and replacement progress.
	collection *toolruntime.Runtime
	// Registry routing and request limits belong to this initialized lifetime.
	registry     *goakt.GrainIdentity
	replyTimeout time.Duration
	// connections owns Session MCP transports; tools borrow them until release.
	connections *connections
	// retries owns transport recovery without acquiring the tool execution operation.
	retries map[string]*connectionRetry
	// request retains only the reply and cancellation of initialization, refresh
	// or close. Replacement progress remains in collection.
	request *operation
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
	for _, request := range owner.retries {
		request.cancel()
		request.reply.Response(Completion{Err: errors.New("tools: Runtime stopped before connection recovery completed")})
	}
	owner.retries = nil
	for _, instance := range owner.actors {
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
	if owner.request != nil {
		if owner.request.cancel != nil {
			owner.request.cancel()
		}
		owner.finish(owner.request, errors.New("tools: runtime stopped before operation completion"))
	}
	return nil
}

// OnReceive serializes protocol handling; framework tasks exclusively run the business owner.
func (owner *Runtime) OnReceive(gc *goakt.GrainContext) {
	if owner.receiveTools(gc) || owner.receiveCalls(gc) || owner.receiveConfiguration(gc) || owner.receiveConnections(gc) {
		return
	}
	switch input := gc.Message().(type) {
	case InitializeRuntime:
		owner.initialize(gc, input)
	case authorized:
		gc.NoErr()
		if owner.request != input.operation {
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
		directory, err := catalogactor.NewClient(gc.ActorSystem(), settings.Registry, settings.Timeout)
		if err != nil {
			owner.finish(input.operation, err)
			return
		}
		owner.collection = business
		instances := owner.newToolClient(gc)
		peers := &connections{
			servers: make(map[string]*mcpclient.Connection),
		}
		if source, ok := gc.ActorSystem().Extension(providerID).(*Provider); ok {
			peers.questions = source.questions
		}
		owner.connections = peers
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
				configuration, err := directory.Read(ctx, settings.AgentID)
				if err != nil {
					return err
				}
				definitions := append(configuration.Global.MCP, configuration.Custom.MCP...)
				if err := peers.reconcile(ctx, definitions); err != nil {
					return err
				}
				configuration.Global, err = peers.catalog(configuration.Global)
				if err != nil {
					return err
				}
				return business.Initialize(ctx, configuration, instances)
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
			owner.collection == nil ||
			owner.request != nil {
			result.Err = errors.New("tools: runtime snapshot is unavailable")
		} else {
			result.Snapshot, result.Err = owner.collection.Snapshot()
		}
		gc.Response(result)
	case ReadDefinitions:
		result := Definitions{}
		if owner.creationID != input.CreationID || owner.collection == nil || owner.request != nil {
			result.Err = errors.New("tools: definitions do not belong to an available runtime")
		} else {
			result.Value, result.Err = owner.collection.Definitions()
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
		if len(owner.retries) != 0 {
			for _, request := range owner.retries {
				request.cancel()
			}
		}
		if owner.request != nil {
			gc.Response(Completion{Err: errors.New("tools: runtime operation is still running")})
			return
		}
		if owner.collection == nil {
			gc.Response(Completion{})
			return
		}
		current := owner.deferOperation(gc)
		if current == nil {
			return
		}
		business := owner.collection
		instances := owner.newToolClient(gc)
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
	if owner.request != nil {
		gc.Response(Completion{Err: errors.New("tools: runtime operation is still running")})
		return
	}
	if owner.creationID != "" {
		var err error
		if owner.creationID != input.CreationID || owner.agentID != input.AgentID {
			err = errors.New("tools: runtime is bound to a different initialization")
		} else if owner.collection == nil {
			err = errors.New("tools: runtime initialization failed")
		} else {
			_, err = owner.collection.Snapshot()
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
	owner.registry = input.Registry
	owner.replyTimeout = input.Timeout
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
	owner.request = current
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
	if owner.request != current {
		return
	}
	if current.cancel != nil {
		current.cancel()
	}
	owner.request = nil
	if current.refreshID != "" {
		owner.changed = true
		current.reply.Response(Definitions{Err: failure})
		return
	}
	current.reply.Response(Completion{Err: failure})
}
