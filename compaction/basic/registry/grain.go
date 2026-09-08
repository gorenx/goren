package compactionregistry

import (
	"context"
	"errors"

	agentactor "github.com/gorenx/goren/agent/actor"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	commandactor "github.com/gorenx/goren/commands/actor"
	"github.com/gorenx/goren/compaction/basic"
	compactionactor "github.com/gorenx/goren/compaction/basic/actor"
	"github.com/gorenx/goren/compaction/toolresultpruner"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type phase uint8

const (
	initializing phase = iota
	available
	failed
	closing
	stopped
)

// instance retains the original grant until business close and deactivation finish.
// It references framework identities only, never the Compaction business object.
type instance struct {
	input    agentactor.StartComponent
	runtime  *goakt.GrainIdentity
	phase    phase
	failure  error
	starters []*goakt.GrainReply
	stoppers []*goakt.GrainReply
	cancel   context.CancelFunc
	// drained preserves confirmed business shutdown across framework-stop retries.
	drained bool
}

// Grain starts and closes per-Agent Compaction instances. Agent membership and
// cross-component ordering belong to the Agent Registry; compression belongs to Basic.
type Grain struct {
	settings *Configure
	// instances maps AgentID to its latest exact component lifetime, including tombstones.
	instances map[session.SessionID]*instance
}

// OnActivate waits for explicit capability configuration.
func (*Grain) OnActivate(context.Context, *goakt.GrainProps) error { return nil }

// OnDeactivate fails outstanding requests without claiming component shutdown.
func (owner *Grain) OnDeactivate(context.Context, *goakt.GrainProps) error {
	for _, entry := range owner.instances {
		if entry.cancel != nil {
			entry.cancel()
		}
		entry.replyStart(errors.New("compaction registry: stopped during startup"))
		entry.replyStop(errors.New("compaction registry: stopped during shutdown"))
	}
	owner.instances = nil
	owner.settings = nil
	return nil
}

// OnReceive serializes grants and schedules capability lifecycle requests.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case Configure:
		gc.Response(Configured{
			Err: owner.configure(input),
		})
	case agentactor.StartComponent:
		owner.start(gc, input)
	case initialized:
		owner.finishStart(gc, input)
	case agentactor.StopComponent:
		owner.stop(gc, input)
	case closed:
		owner.finishStop(gc, input)
	default:
		gc.Unhandled()
	}
}

func (owner *Grain) configure(input Configure) error {
	if owner.settings != nil ||
		input.Commands == nil ||
		input.Commands.Name() != commandactor.RegistryName ||
		input.Meter == nil ||
		input.Router == nil ||
		input.Timeout <= 0 {
		return errors.New("compaction registry: fresh configuration and complete routes are required")
	}
	if _, err := basic.ResolveConfig(input.Config); err != nil {
		return err
	}
	input.Config = input.Config.Clone()
	if input.Pruner != nil {
		if _, err := toolresultpruner.ResolveConfig(*input.Pruner); err != nil {
			return err
		}
		pruning := input.Pruner.Clone()
		input.Pruner = &pruning
	}
	owner.settings = &input
	return nil
}

func (owner *Grain) start(gc *goakt.GrainContext, input agentactor.StartComponent) {
	if owner.settings == nil ||
		input.AgentID == "" ||
		input.CreationID == "" ||
		input.Agent == nil ||
		input.Session == nil ||
		input.Agent.Name() != loopactor.RuntimeName(string(input.AgentID)) ||
		input.Session.Name() != sessionactor.RuntimeName(string(input.AgentID)) {
		gc.Response(agentactor.ComponentStarted{
			Err: errors.New("compaction registry: configured owner and matching routes are required"),
		})
		return
	}
	if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))); err != nil {
		gc.Response(agentactor.ComponentStarted{
			Err: err,
		})
		return
	}
	entry := owner.instances[input.AgentID]
	if entry != nil && (entry.phase != stopped || entry.input.CreationID == input.CreationID) {
		if entry.input.CreationID != input.CreationID ||
			!entry.input.Agent.Equal(input.Agent) ||
			!entry.input.Session.Equal(input.Session) ||
			entry.phase == closing ||
			entry.phase == stopped ||
			len(entry.stoppers) != 0 {
			gc.Response(agentactor.ComponentStarted{
				Err: errors.New("compaction registry: creation is not available"),
			})
			return
		}
		if entry.phase != initializing {
			result := agentactor.ComponentStarted{
				Err: entry.failure,
			}
			if result.Err == nil {
				result.Runtime = entry.runtime
			}
			gc.Response(result)
			return
		}
	} else {
		entry = &instance{
			input: input,
			phase: initializing,
		}
		if owner.instances == nil {
			owner.instances = make(map[session.SessionID]*instance)
		}
		owner.instances[input.AgentID] = entry
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(agentactor.ComponentStarted{
			Err: errors.New("compaction registry: startup requires a request"),
		})
		return
	}
	entry.starters = append(entry.starters, reply)
	if entry.cancel != nil {
		return
	}
	identity, err := goakt.GrainOf[*compactionactor.Grain](gc.Context(), gc.ActorSystem(), compactionactor.RuntimeName(string(input.AgentID)), goakt.WithLongLivedGrain())
	entry.runtime = identity
	if err != nil {
		entry.phase = failed
		entry.failure = err
		entry.replyStart(err)
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	entry.cancel = cancel
	completed := make(chan error, 1)
	if err := gc.PipeToSelf(func() (any, error) {
		select {
		case err := <-completed:
			return initialized{
				entry: entry,
				err:   err,
			}, nil
		case <-ctx.Done():
			return initialized{
				entry: entry,
				err:   context.Cause(ctx),
			}, nil
		}
	}); err != nil {
		cancel()
		entry.cancel = nil
		entry.phase = failed
		entry.failure = err
		entry.replyStart(err)
		return
	}
	settings := owner.settings
	gc.RequestGrain(identity, compactionactor.Initialize{
		Commands:   settings.Commands,
		Agent:      input.Agent,
		AgentID:    input.AgentID,
		Session:    input.Session,
		CreationID: input.CreationID,
		Meter:      settings.Meter,
		Router:     settings.Router,
		Config:     settings.Config.Clone(),
		Pruner:     settings.Pruner,
		Timeout:    settings.Timeout,
	}, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
		if failure == nil {
			result, ok := payload.(compactionactor.Completion)
			if !ok {
				failure = errors.New("compaction registry: invalid initialization reply")
			} else {
				failure = result.Err
			}
		}
		completed <- failure
	})
}

type initialized struct {
	entry *instance
	err   error
}

func (owner *Grain) finishStart(gc *goakt.GrainContext, input initialized) {
	entry := input.entry
	if owner.instances[entry.input.AgentID] != entry || entry.phase != initializing {
		return
	}
	entry.cancel()
	entry.cancel = nil
	entry.failure = input.err
	entry.phase = available
	if input.err != nil {
		entry.phase = failed
	}
	if len(entry.stoppers) != 0 {
		entry.replyStart(errors.Join(input.err, errors.New("compaction registry: stopped during startup")))
		owner.runStop(gc, entry)
		return
	}
	entry.replyStart(input.err)
}

func (entry *instance) replyStart(failure error) {
	result := agentactor.ComponentStarted{
		Err: failure,
	}
	if failure == nil {
		result.Runtime = entry.runtime
	}
	for _, reply := range entry.starters {
		reply.Response(result)
	}
	entry.starters = nil
}
