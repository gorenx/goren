package companionactor

import (
	"context"
	"errors"
	"sort"

	"github.com/gorenx/goren/agent"
	agentregistry "github.com/gorenx/goren/agent/registry"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/subagent/companion"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// lifecycle governs management admission and whether another parent can bind.
type lifecycle uint8

const (
	uninitialized lifecycle = iota
	initializing
	active
	shuttingDown
	terminated
)

// steward owns management operations, never child execution or resource ledgers.
type steward struct {
	loadReply  *goakt.GrainReply
	input      Initialization
	entries    map[string]*resident
	state      lifecycle
	waiters    []*goakt.GrainReply
	loadCancel context.CancelFunc
	deliveries map[*delivery]struct{}
}

type resident struct {
	record   companion.Companion
	mutation *mutation
	creation agentregistry.Creation
	executor *goakt.GrainIdentity
	starting *startup
	closing  *shutdown
	phase    Phase
	failure  error
}

type loaded struct {
	entries []companion.Companion
	err     error
	reply   *goakt.GrainReply
}

func (*steward) OnActivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *steward) OnDeactivate(context.Context, *goakt.GrainProps) error {
	if owner.loadCancel != nil {
		owner.loadCancel()
	}
	failure := errors.New("companion: Steward stopped before management completed")
	if owner.loadReply != nil {
		owner.loadReply.Response(response{
			err: failure,
		})
		owner.loadReply = nil
	}
	for _, entry := range owner.entries {
		if entry.starting != nil {
			entry.starting.cancel()
		}
		if entry.closing != nil {
			entry.closing.cancel()
		}
		if entry.mutation != nil {
			entry.mutation.respond(entry.view(), failure)
		}
	}
	for _, reply := range owner.waiters {
		reply.Response(response{
			err: failure,
		})
	}
	owner.waiters = nil
	for operation := range owner.deliveries {
		operation.cancel()
		operation.reply.Response(response{
			err: failure,
		})
	}
	owner.deliveries = nil
	return nil
}

func (owner *steward) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case Initialization:
		owner.initialize(gc, input)
	case loaded:
		owner.loaded(gc, input)
	case recorded:
		owner.recorded(gc, input)
	case started:
		owner.started(gc, input)
	case closed:
		owner.closed(gc, input)
	case delivered:
		owner.delivered(input)
	case command:
		if input.parent != owner.input.Parent || owner.input.Parent.AgentID == "" {
			gc.Response(response{
				err: agentregistry.ErrCreation,
			})
			return
		}
		if _, ok := input.input.(closing); ok {
			owner.close(gc)
			return
		}
		if owner.state == uninitialized || owner.state == initializing {
			gc.Response(response{
				err: agentregistry.ErrCreation,
			})
			return
		}
		if _, ok := input.input.(listing); ok {
			gc.Response(response{
				items: owner.list(),
			})
			return
		}
		if owner.state != active {
			gc.Response(response{
				err: agent.ErrClosing,
			})
			return
		}
		switch request := input.input.(type) {
		case Create:
			owner.create(gc, request)
		case Configure:
			owner.configure(gc, request)
		case Remove:
			owner.remove(gc, request)
		case Restart:
			owner.restart(gc, request)
		case sendMessage:
			owner.sendMessage(gc, request)
		case reportMessage:
			owner.reportMessage(gc, request)
		default:
			gc.Unhandled()
		}
	default:
		gc.Unhandled()
	}
}

func (owner *steward) initialize(gc *goakt.GrainContext, input Initialization) {
	if owner.state != uninitialized && owner.state != terminated {
		gc.Response(response{
			err: companion.ErrConflict,
		})
		return
	}
	if input.Creator == nil || input.Settings == nil || input.Registry == nil || input.Session == nil || input.Parent.AgentID == "" || input.Parent.CreationID == "" {
		gc.Response(response{
			err: companion.ErrInvalid,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(response{
			err: errors.New("companion: initialization requires a request"),
		})
		return
	}
	*owner = steward{
		input:   input,
		state:   initializing,
		entries: make(map[string]*resident),
	}
	ctx, cancel := context.WithCancel(context.Background())
	owner.loadCancel = cancel
	owner.loadReply = reply
	system := gc.ActorSystem()
	err := gc.PipeToSelf(func() (any, error) {
		inspected, failure := ask[agentregistry.Inspected](ctx, system, input.Registry, agentregistry.Inspect{
			Creation: input.Parent,
		})
		if failure == nil {
			failure = inspected.Err
		}
		if failure == nil && inspected.Registration.Role == agent.CompanionRole {
			failure = agent.ErrRole
		}
		if failure == nil {
			ready, readyErr := ask[agentregistry.Ready](ctx, system, input.Registry, agentregistry.FindAgent{
				AgentID: input.Parent.AgentID,
			})
			failure = errors.Join(readyErr, ready.Err)
			if failure == nil && ready.Creation != input.Parent {
				failure = agentregistry.ErrCreation
			}
		}
		var entries []companion.Companion
		if failure == nil {
			snapshot, readErr := ask[sessionactor.View](ctx, system, input.Session, sessionactor.Read{
				CreationID: input.Parent.CreationID,
			})
			failure = errors.Join(readErr, snapshot.Err)
			if failure == nil && snapshot.Snapshot.Barrier.SessionID != input.Parent.AgentID {
				failure = companion.ErrInvalid
			}
			if failure == nil {
				entries, failure = companion.CompanionsFromEvents(input.Parent.AgentID, snapshot.Snapshot.Events)
			}
		}
		return loaded{
			entries: entries,
			err:     failure,
			reply:   reply,
		}, nil
	})
	if err != nil {
		cancel()
		owner.state = uninitialized
		owner.loadReply = nil
		reply.Response(response{
			err: err,
		})
	}
}

func (owner *steward) loaded(gc *goakt.GrainContext, result loaded) {
	if result.reply != owner.loadReply {
		return
	}
	if owner.loadCancel != nil {
		owner.loadCancel()
		owner.loadCancel = nil
	}
	owner.loadReply = nil
	if owner.state == shuttingDown {
		result.reply.Response(response{
			err: agent.ErrClosing,
		})
		owner.finishClose()
		return
	}
	owner.state = uninitialized
	if result.err != nil {
		result.reply.Response(response{
			err: result.err,
		})
		return
	}
	owner.state = active
	for _, entry := range result.entries {
		retained := &resident{
			record: entry,
			phase:  Stopped,
		}
		owner.entries[entry.Name] = retained
		owner.reconcile(gc, retained)
	}
	result.reply.Response(response{})
}

func (entry *resident) view() View {
	record := entry.record
	if record.Name == "" && entry.mutation != nil {
		record = entry.mutation.next
	}
	failure := entry.failure
	if entry.mutation != nil && entry.mutation.failure != nil {
		failure = entry.mutation.failure
	}
	return View{
		Persisted: entry.record.Name != "",
		Companion: record.Clone(),
		Creation:  entry.creation,
		Phase:     entry.phase,
		Err:       failure,
	}
}

func (owner *steward) list() []View {
	result := make([]View, 0, len(owner.entries))
	for _, entry := range owner.entries {
		result = append(result, entry.view())
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Companion.Name < result[right].Companion.Name })
	return result
}
