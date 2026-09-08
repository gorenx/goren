package compactionactor

import (
	"errors"

	"github.com/google/uuid"
	commandactor "github.com/gorenx/goren/commands/actor"
	"github.com/gorenx/goren/compaction/basic"
	compact "github.com/gorenx/goren/compaction/command"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// commandRegistration owns publication identity and registration scheduling only.
// It does not own command executions or the Agent maintenance route.
type commandRegistration struct {
	// registry is the already-started global Commands Registry supplied by composition.
	registry *goakt.GrainIdentity
	// id identifies this publication attempt. All retries and withdrawal reuse it;
	// it is independent of business CommandID and Agent CreationID.
	id string
	// timer is the framework retry timer reference, cancelled on acknowledgement
	// or closure. Already queued ticks are rejected by phase and object identity.
	timer string
	// phase distinguishes publishing, acknowledged publication and permanent
	// admission closure while withdrawal is pending or needs retry.
	phase registrationPhase
}

type registrationPhase uint8

const (
	registrationPending registrationPhase = iota
	registrationPublished
	registrationClosing
)

type registerCommand struct{ publication *commandRegistration }

// startCommand schedules retries immediately after capability initialization.
func (owner *Grain) startCommand(gc *goakt.GrainContext, registry *goakt.GrainIdentity) error {
	current := &commandRegistration{
		registry: registry,
		id:       uuid.NewString(),
	}
	owner.command = current
	timer, err := gc.Schedule(registerCommand{
		publication: current,
	}, owner.timeout)
	if err != nil {
		owner.command = nil
		return err
	}
	current.timer = timer
	owner.registerCommand(gc, current)
	return nil
}

func (owner *Grain) registerCommand(gc *goakt.GrainContext, current *commandRegistration) {
	if owner.command != current ||
		current.phase != registrationPending {
		return
	}
	activation := owner.activation
	gc.RequestGrain(current.registry,
		commandactor.Advertise{
			ID:         current.id,
			SessionID:  owner.agentID,
			Definition: compact.Definition(),
			Handler:    gc.Self(),
		},
		goakt.WithRequestTimeout(owner.timeout),
		goakt.WithReentrancyMode(reentrancy.AllowAll),
	).Then(
		func(payload any, failure error) {
			if owner.command != current ||
				current.phase != registrationPending {
				return
			}
			result, ok := payload.(commandactor.Registered)
			if failure != nil ||
				!ok ||
				result.Err != nil ||
				result.ID != current.id {
				return
			}
			current.phase = registrationPublished
			_ = activation.CancelSchedule(current.timer)
		},
	)
}

// invocationReply is internal reply erasure shared by middleware and commands.
type invocationReply interface {
	Response(any)
	Err(error)
}

type commandReply struct{ reply *goakt.GrainReply }

func (target commandReply) Response(payload any) {
	result, ok := payload.(Result)
	if !ok {
		target.Err(errors.New("compaction: invalid command result"))
		return
	}
	output, failure := compact.RenderResult(result.Outcome.Latest, result.Err)
	target.reply.Response(commandactor.Completed{
		Result: output,
		Err:    failure,
	})
}
func (target commandReply) Err(failure error) { target.reply.Err(failure) }

func (owner *Grain) receiveCommand(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case commandWithdrawn:
		gc.NoErr()
		if input.err == nil && owner.command == input.publication {
			owner.command = nil
			owner.closeResources(gc)
		}
		input.reply.Response(Completion{
			Err: input.err,
		})
	case registerCommand:
		gc.NoErr()
		owner.registerCommand(gc, input.publication)
	case commandactor.Invoke:
		if owner.command == nil ||
			owner.command.phase == registrationClosing ||
			input.CreationID != owner.creationID ||
			input.Input.SessionID != owner.agentID ||
			input.Input.CommandID == "" {
			gc.Response(commandactor.Completed{
				Err: errors.New("compaction: current registered command identity is required"),
			})
			return true
		}
		if err := compact.ValidateInput(input.Input.RawInput); err != nil {
			output, failure := compact.RenderResult(nil, err)
			gc.Response(commandactor.Completed{
				Result: output,
				Err:    failure,
			})
			return true
		}
		reply := gc.DeferResponse()
		if reply == nil {
			gc.Err(errors.New("compaction: command requires a reply-capable request"))
			return true
		}
		origin := string(input.Input.CommandID)
		owner.execute(gc, Execute{
			Agent:           owner.executor,
			RequestID:       uuid.NewString(),
			Mode:            basic.Manual,
			SourceCommandID: &origin,
		}, commandReply{
			reply: reply,
		}, input.Input.CommandID)
	case commandactor.CancelInvocation:
		current := owner.active
		if current == nil ||
			input.CreationID != owner.creationID ||
			current.commandID != input.CommandID ||
			current.commandID == "" {
			gc.Response(commandactor.Cancelled{})
			return true
		}
		gc.Response(commandactor.Cancelled{
			Err: owner.cancelInvocation(gc, current, input.Cause),
		})
	default:
		return false
	}
	return true
}

type commandWithdrawn struct {
	publication *commandRegistration
	reply       *goakt.GrainReply
	err         error
}

func (owner *Grain) closeCommand(gc *goakt.GrainContext) {
	current := owner.command
	current.phase = registrationClosing
	_ = gc.CancelSchedule(current.timer)
	reply := gc.DeferResponse()
	activation := owner.activation
	gc.RequestGrain(current.registry, commandactor.Unregister{
		ID: current.id,
	}, goakt.WithRequestTimeout(owner.timeout), goakt.WithReentrancyMode(reentrancy.AllowAll)).Then(func(payload any, failure error) {
		_, ok := payload.(commandactor.Unregistered)
		if failure == nil && !ok {
			failure = errors.New("compaction: invalid command withdrawal reply")
		}
		if _, err := activation.ScheduleOnce(commandWithdrawn{
			publication: current,
			reply:       reply,
			err:         failure,
		}, 0); err != nil {
			reply.Response(Completion{
				Err: err,
			})
		}
	})
}
