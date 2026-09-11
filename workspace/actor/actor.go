// Package workspaceactor serializes Workspace operations and publishes notices.
package workspaceactor

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/gorenx/goren/workspace"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Actor owns one Registry and its Backend. I/O runs in one framework task;
// the framework stash preserves operation order until that task settles.
type Actor struct {
	backend  workspace.Backend
	options  workspace.RegistryOptions
	registry *workspace.DurableRegistry
	active   *task
}

// Spawn starts the single Workspace owner with framework stashing enabled.
// Storage remains owned by the caller if actor startup fails.
func Spawn(
	ctx context.Context, system goakt.ActorSystem,
	name string, storage workspace.Backend,
	options workspace.RegistryOptions,
) (*goakt.PID, error) {
	if system == nil {
		return nil, errors.New("workspace: actor system is required")
	}
	return system.Spawn(ctx, name, &Actor{
		backend: storage,
		options: options,
	}, goakt.WithStashing(), goakt.WithLongLived())
}

// PreStart restores business state before accepting any operation.
func (owner *Actor) PreStart(ctx *goakt.Context) error {
	directory, err := workspace.NewRegistry(ctx.Context(), owner.backend, owner.options)
	if err != nil {
		return err
	}
	owner.registry = directory
	return nil
}

// PostStop closes storage after mailbox processing has stopped.
func (owner *Actor) PostStop(ctx *goakt.Context) error {
	if owner.active != nil {
		owner.active.cancel(context.Canceled)
		<-owner.active.done
		owner.active.receiver.Deliver(owner.active.result)
		owner.active = nil
	}
	if owner.registry == nil {
		return nil
	}
	return owner.registry.Close(context.WithoutCancel(ctx.Context()))
}

// Receive serializes asynchronous business operations using the framework stash.
func (owner *Actor) Receive(rc *goakt.ReceiveContext) {
	switch input := rc.Message().(type) {
	case *goakt.PostStart:
		return
	case Request:
		if owner.active != nil {
			rc.Stash()
			return
		}
		if input.Receiver == nil || input.Command == nil {
			rc.Response(Completion{
				Err: errors.New("workspace: command and result receiver are required"),
			})
			return
		}
		ctx := input.Context
		if ctx == nil {
			ctx = context.WithoutCancel(rc.Context())
		}
		work, cancel := context.WithCancelCause(ctx)
		current := &task{
			receiver: input.Receiver,
			cancel:   cancel,
			done:     make(chan struct{}),
		}
		owner.active = current
		rc.Response(Completion{})
		if err := rc.Self().PipeTo(rc.Context(), rc.Self(), func() (any, error) {
			defer close(current.done)
			current.result = owner.execute(work, input.Command)
			current.notices = owner.registry.TakeNotices()
			return finished{
				task: current,
			}, nil
		}); err != nil {
			current.cancel(err)
			owner.active = nil
			current.receiver.Deliver(Completion{
				Err: err,
			})
			rc.UnstashAll()
		}
	case finished:
		if owner.active != input.task {
			return
		}
		current := owner.active
		owner.active = nil
		current.cancel(nil)
		for _, notice := range current.notices {
			if topic := rc.ActorSystem().TopicActor(); topic != nil {
				if err := goakt.Tell(rc.Context(), topic, goakt.NewPublish(uuid.NewString(), workspace.Topic, notice)); err != nil {
					rc.Logger().Error(err)
				}
			}
		}
		current.receiver.Deliver(current.result)
		rc.UnstashAll()
	default:
		rc.Unhandled()
	}
}

func (owner *Actor) execute(ctx context.Context, command Command) Reply {
	if err := ctx.Err(); err != nil {
		return Completion{
			Err: context.Cause(ctx),
		}
	}
	var result Reply
	switch input := command.(type) {
	case Create:
		subject, created, err := owner.registry.Create(ctx, input.Path)
		response := Created{
			Created: created,
			Err:     err,
		}
		if err == nil {
			response.State = subject.Snapshot()
		}
		result = response
	case Read:
		subject, found := owner.registry.Get(input.ID)
		response := View{
			Found: found,
		}
		if found {
			response.State = subject.Snapshot()
			response.Status = subject.Status()
		}
		result = response
	case List:
		entries := owner.registry.List()
		states := make([]workspace.WorkspaceState, 0, len(entries))
		for _, subject := range entries {
			states = append(states, subject.Snapshot())
		}
		result = Listing{
			Workspaces:         states,
			ArchivedSessionIDs: owner.registry.ArchivedSessionIDs(),
		}
	case Delete:
		removed, err := owner.registry.Delete(ctx, input.ID)
		result = Deleted{
			Removed: removed,
			Err:     err,
		}
	case InsertBefore:
		order, err := owner.registry.InsertBefore(ctx, input.ID, input.Before)
		result = Order{
			IDs: order,
			Err: err,
		}
	case Rename:
		err := owner.registry.SetTitle(ctx, input.ID, input.Title)
		result = owner.updated(input.ID, err)
	case AttachSession:
		result = Completion{
			Err: owner.registry.AttachSession(ctx, input.ID, input.SessionID),
		}
	case DetachSession:
		result = Completion{
			Err: owner.registry.DetachSession(ctx, input.ID, input.SessionID),
		}
	case InsertSessionBefore:
		err := owner.registry.InsertSessionBefore(ctx, input.ID, input.SessionID, input.Before)
		result = owner.updated(input.ID, err)
	case ArchiveSession:
		err := owner.registry.ArchiveSession(ctx, input.SessionID)
		result = Archived{
			IDs: owner.registry.ArchivedSessionIDs(),
			Err: err,
		}
	default:
		return Completion{
			Err: errors.New("workspace: unsupported command"),
		}
	}
	return result
}

type task struct {
	receiver Receiver
	cancel   context.CancelCauseFunc
	done     chan struct{}
	result   Reply
	notices  []workspace.Notice
}
type finished struct{ task *task }

func (owner *Actor) updated(identifier workspace.ID, failure error) Updated {
	response := Updated{
		Err: failure,
	}
	if failure == nil {
		subject, _ := owner.registry.Get(identifier)
		response.State = subject.Snapshot()
	}
	return response
}
