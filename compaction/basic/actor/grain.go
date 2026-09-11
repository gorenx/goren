package compactionactor

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/agent"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	"github.com/gorenx/goren/commands"
	commandactor "github.com/gorenx/goren/commands/actor"
	"github.com/gorenx/goren/compaction/basic"
	"github.com/gorenx/goren/compaction/toolresultpruner"
	llmactor "github.com/gorenx/goren/llm/actor"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// Grain schedules one Agent's Compaction operations. Business stages and
// completion decisions belong to Compaction and Transaction, not this adapter.
type Grain struct {
	activation *goakt.GrainProps
	agentID    session.SessionID
	// executor is this Agent creation's maintenance owner, independent of command registration.
	executor     *goakt.GrainIdentity
	conversation *goakt.GrainIdentity
	creationID   string
	meter        *goakt.GrainIdentity
	models       *llmactor.Client
	timeout      time.Duration
	business     *basic.Compaction
	pruner       *toolresultpruner.ToolResultPruner
	active       *invocation
	recovery     *basic.Recovery
	diagnostics  basic.Diagnostics
	command      *commandRegistration
}

// invocation retains only request delivery and dependency results for one call.
type invocation struct {
	failure    agent.RequestFailure
	pruneError error
	reply      invocationReply
	commandID  commands.ID
	input      Execute
	snapshot   session.Snapshot
	prices     tokenmeter.Measurement
	callID     llmactor.CallID
	hook       *middlewareactor.Invocation
	// cancelled retains the accepted cause while asynchronous preparation is pending.
	cancelled error
}

type continuation struct {
	call *invocation
	run  func(*goakt.GrainContext)
}

// OnActivate allocates no business resources before initialization.
func (owner *Grain) OnActivate(_ context.Context, props *goakt.GrainProps) error {
	owner.activation = props
	return nil
}

// OnDeactivate reports delivery failure, never a settled Compaction Result.
// Production lifecycle must close the binding before deactivating this Grain.
func (owner *Grain) OnDeactivate(context.Context, *goakt.GrainProps) error {
	if owner.active != nil {
		owner.active.reply.Err(errors.New("compaction: Grain stopped during invocation"))
		owner.active = nil
	}
	return nil
}

// OnReceive dispatches typed requests and scheduled dependency completions.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	if owner.receiveCommand(gc) ||
		owner.receiveBinding(gc) ||
		owner.receiveInvocation(gc) ||
		owner.receiveRequest(gc) {
		return
	}
	if input, ok := gc.Message().(continuation); ok {
		gc.NoErr()
		if owner.active == input.call {
			input.run(gc)
		}
		return
	}
	if owner.models != nil && owner.models.Receive(gc, llmactor.Receivers{
		Generation:   owner,
		Chunk:        owner,
		Close:        owner,
		Model:        owner,
		Cancellation: owner,
	}) {
		return
	}
	gc.Unhandled()
}

func (owner *Grain) initialize(gc *goakt.GrainContext, input Initialize) error {
	if owner.conversation != nil ||
		input.AgentID == "" ||
		gc.Self().Name() != RuntimeName(string(input.AgentID)) ||
		input.Session == nil ||
		input.Agent == nil ||
		input.Commands == nil ||
		input.Commands.Name() != commandactor.RegistryName ||
		input.Meter == nil ||
		input.Router == nil ||
		input.CreationID == "" ||
		input.Timeout <= 0 {
		return errors.New("compaction: fresh binding, matching Agent identity, dependency routes and positive timeout are required")
	}
	settings, err := basic.ResolveConfig(input.Config)
	if err != nil {
		return err
	}
	business, err := basic.NewCompaction(settings, &tokenmeter.TokenMeter{})
	if err != nil {
		return err
	}
	var pruner *toolresultpruner.ToolResultPruner
	if input.Pruner != nil {
		pruner, err = toolresultpruner.New(*input.Pruner)
		if err != nil {
			return err
		}
	}
	endpoint, err := llmactor.NewClient(input.Router, input.Timeout)
	if err != nil {
		return err
	}
	if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))); err != nil {
		return err
	}
	recovery, err := basic.NewRecovery(input.AgentID, settings)
	if err != nil {
		return err
	}
	owner.recovery = recovery
	owner.agentID = input.AgentID
	owner.executor = input.Agent
	owner.conversation = input.Session
	owner.creationID = input.CreationID
	owner.meter = input.Meter
	owner.models = endpoint
	owner.timeout = input.Timeout
	owner.business = business
	owner.pruner = pruner
	return owner.startCommand(gc, input.Commands)
}

func (owner *Grain) finish(gc *goakt.GrainContext, current *invocation, outcome basic.Outcome, failure error) {
	if owner.active != current {
		return
	}
	if current.hook != nil {
		owner.completeHook(gc, current, failure)
		return
	}
	if current.input.Mode == basic.Manual {
		owner.finishMaintenance(gc, current, outcome, failure)
		return
	}
	owner.active = nil
	current.reply.Response(Result{
		Outcome: outcome,
		Err:     failure,
	})
}

func (owner *Grain) later(gc *goakt.GrainContext, current *invocation, run func(*goakt.GrainContext)) {
	// Retrying delivery does not complete the original request or change business state.
	if _, err := gc.ScheduleOnce(continuation{
		call: current,
		run:  run,
	}, 100*time.Millisecond); err != nil {
		gc.Err(err)
	}
}
