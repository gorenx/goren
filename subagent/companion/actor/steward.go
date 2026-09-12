package companionactor

import (
	"context"
	"errors"
	"sort"

	"github.com/gorenx/goren/agent"
	agentregistry "github.com/gorenx/goren/agent/registry"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/settings"
	companions "github.com/gorenx/goren/subagent/companion"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// lifecycle governs management admission through recovery, service and shutdown.
type lifecycle uint8

const (
	uninitialized lifecycle = iota
	initializing
	active
	shuttingDown
	terminated
)

// companion is one managed Companion, with its relationship, current child
// lifetime and unsettled management operations. Steward serializes its changes.
type companion struct {
	// candidate identifies an automatic policy whose preparation failed before it could be recorded.
	candidate companions.Companion
	// relationship is the confirmed Companion relationship; mutation owns the first uncommitted target.
	relationship companions.Companion
	// mutation retains an unsettled management change and its frozen retry input.
	mutation *mutation
	// creation identifies the current or unsettled child lifetime and clears only after closure.
	creation agentregistry.Creation
	// executor addresses the child AgentLoop used to drain its current work.
	executor *goakt.GrainIdentity
	// starting retains child creation until its result settles, even after cancellation.
	starting *startup
	// closing retains the active child close attempt; failure preserves its lifetime for retry.
	closing *shutdown
	// phase describes execution and cleanup progress serialized by Steward.
	phase Phase
	// failure retains the child startup or closure error until a retry or state transition handles it.
	failure error
}

// recovery owns cancellation and result identity for one recovery operation.
type recovery struct {
	// cancel requests identity-check and log-read cancellation; the matching loaded result settles recovery.
	cancel context.CancelFunc
}

type loaded struct {
	// entries contains relationships projected from the parent's own log, without runtime routes.
	entries []companions.Companion
	// defaults contains Settings policies fixed after the relationship read, including preparation failures.
	defaults []automatic
	// err reports parent identity, log read or relationship projection failure.
	err error
	// operation identifies the recovery that produced this result and fences late completions.
	operation *recovery
}

// steward owns management operations, never child execution or resource ledgers.
type steward struct {
	// recovery retains the active relationship load; shutdown cancels it and waits for settlement.
	recovery *recovery
	// failure belongs to Steward recovery or overall shutdown, independently of child failures.
	failure error
	// parent fixes the exact lifetime to which this management capability is bound.
	parent agentregistry.Creation
	// registry is the borrowed Agent Registry endpoint owning identity and resource ledgers.
	registry *goakt.GrainIdentity
	// session is the borrowed parent Session endpoint storing relationship facts.
	session *goakt.GrainIdentity
	// creator borrows ordinary child creation using parent, child and configuration identities.
	creator ChildCreator
	// settings owns named configurations; relationship events retain only their names.
	settings settings.Settings
	// entries indexes managed Companions by name, including uncommitted and removed relationships.
	entries map[string]*companion
	// state is Steward's single lifecycle authority for management admission and shutdown.
	state lifecycle
	// waiters await overall closure after in-flight operations and children have settled.
	waiters []*goakt.GrainReply
	// deliveries retains ordinary Inbox requests whose results shutdown must cancel and await.
	deliveries map[*delivery]struct{}
}

func (*steward) OnActivate(context.Context, *goakt.GrainProps) error { return nil }

func (owner *steward) OnDeactivate(context.Context, *goakt.GrainProps) error {
	failure := errors.New("companion: Steward stopped before management completed")
	if owner.recovery != nil {
		owner.recovery.cancel()
		owner.recovery = nil
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
	case agentregistry.RestartChild:
		owner.restartConfiguredChild(gc, input)
	case start:
		owner.start(gc, input)
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
		owner.receiveCommand(gc, input)
	default:
		gc.Unhandled()
	}
}

// receiveCommand applies parent identity and lifecycle admission before dispatch.
func (owner *steward) receiveCommand(gc *goakt.GrainContext, input command) {
	// A component may stop before asynchronous initialization reached this Grain.
	if _, ok := input.input.(closing); ok &&
		owner.state == uninitialized && owner.parent.AgentID == "" &&
		input.parent.AgentID != "" && input.parent.CreationID != "" {
		owner.parent = input.parent
		owner.state = terminated
	}
	if input.parent != owner.parent || owner.parent.AgentID == "" {
		gc.Response(response{
			err: agentregistry.ErrCreation,
		})
		return
	}
	if _, ok := input.input.(closing); ok {
		owner.close(gc)
		return
	}
	if _, ok := input.input.(inspection); ok {
		gc.Response(response{
			status: owner.status(),
		})
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
}

func (owner *steward) start(gc *goakt.GrainContext, input start) {
	if input.Creator == nil ||
		input.Settings == nil ||
		input.Registry == nil ||
		input.Session == nil ||
		input.Parent.AgentID == "" ||
		input.Parent.CreationID == "" {
		gc.Response(response{
			err: companions.ErrInvalid,
		})
		return
	}
	// Repeated installation never restarts recovery or changes its dependencies.
	if owner.parent.AgentID != "" {
		failure := companions.ErrConflict
		if owner.parent == input.Parent && owner.state != shuttingDown && owner.state != terminated &&
			owner.registry.Equal(input.Registry) && owner.session.Equal(input.Session) {
			failure = nil
		}
		gc.Response(response{
			err: failure,
		})
		return
	}
	gc.Response(response{
		err: owner.prepare(gc, input),
	})
}

func (owner *steward) prepare(gc *goakt.GrainContext, input start) error {
	ctx, cancel := context.WithCancel(context.Background())
	operation := &recovery{
		cancel: cancel,
	}
	*owner = steward{
		parent:   input.Parent,
		registry: input.Registry,
		session:  input.Session,
		creator:  input.Creator,
		settings: input.Settings,
		state:    initializing,
		entries:  make(map[string]*companion),
		recovery: operation,
	}
	system := gc.ActorSystem()
	err := gc.PipeToSelf(func() (any, error) {
		ready, failure := ask[agentregistry.Ready](ctx, system, input.Registry, agentregistry.FindAgent{
			AgentID:   input.Parent.AgentID,
			WaitReady: true,
		})
		if failure == nil {
			failure = ready.Err
		}
		if failure == nil && ready.Creation != input.Parent {
			failure = agentregistry.ErrCreation
		}
		if failure == nil && ready.Registration.Role == agent.CompanionRole {
			failure = agent.ErrRole
		}
		var entries []companions.Companion
		var defaults []automatic
		if failure == nil {
			snapshot, readErr := ask[sessionactor.View](ctx, system, input.Session, sessionactor.Read{
				CreationID: input.Parent.CreationID,
			})
			failure = errors.Join(readErr, snapshot.Err)
			if failure == nil && snapshot.Snapshot.Barrier.SessionID != input.Parent.AgentID {
				failure = companions.ErrInvalid
			}
			if failure == nil {
				entries, failure = companions.CompanionsFromEvents(input.Parent.AgentID, snapshot.Snapshot.Events)
			}
		}
		if failure == nil {
			view, readErr := input.Settings.Describe(ctx)
			failure = readErr
			if failure == nil {
				defaults = defaultPolicies(ctx, input.Parent, entries, view, input.Creator)
			}
		}
		return loaded{
			entries:   entries,
			defaults:  defaults,
			err:       failure,
			operation: operation,
		}, nil
	})
	if err != nil {
		cancel()
		owner.state = uninitialized
		owner.recovery = nil
		owner.failure = err
	}
	return err
}

func (owner *steward) loaded(gc *goakt.GrainContext, result loaded) {
	if result.operation != owner.recovery {
		return
	}
	result.operation.cancel()
	owner.recovery = nil
	if owner.state == shuttingDown {
		owner.finishClose()
		return
	}
	owner.state = uninitialized
	owner.failure = result.err
	if result.err != nil {
		return
	}
	owner.state = active
	for _, entry := range result.entries {
		retained := &companion{
			relationship: entry,
			phase:        Stopped,
		}
		owner.entries[entry.Name] = retained
		owner.reconcile(gc, retained)
	}
	for _, candidate := range result.defaults {
		owner.installDefault(gc, candidate)
	}
}

func (owner *steward) status() Status {
	current := Status{
		Err: owner.failure,
	}
	switch owner.state {
	case initializing:
		current.Phase = Starting
	case active:
		current.Phase = Ready
	case shuttingDown:
		current.Phase = Stopping
		if current.Err != nil {
			current.Phase = CloseFailed
		}
	case terminated:
		current.Phase = Stopped
	case uninitialized:
		current.Phase = StartFailed
	}
	return current
}

func (entry *companion) view() View {
	relationship := entry.relationship
	if relationship.Name == "" && entry.mutation != nil {
		relationship = entry.mutation.next
	} else if relationship.Name == "" {
		relationship = entry.candidate
	}
	failure := entry.failure
	if entry.mutation != nil && entry.mutation.failure != nil {
		failure = entry.mutation.failure
	}
	return View{
		Persisted: entry.relationship.Name != "",
		Companion: relationship.Clone(),
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
