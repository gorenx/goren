package session

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/plugin"
)

// ErrNotAttached reports that an exact Session does not own a membership in
// the receiving LiveStore.
var ErrNotAttached = errors.New("session: Session is not attached to this Store")

const (
	// PluginName is the canonical Session capability plugin name.
	PluginName = "@deepseek-ai/dsh-session"
	// CreatedEventName identifies the vetoable live membership publication edge.
	CreatedEventName = "session/created"
	// DisposedEventName identifies cleanup after an announced membership is removed.
	DisposedEventName = "session/disposed"
	// AppendedEventName identifies a Session Event already committed to memory.
	AppendedEventName = "session/event"
	// FlushEventName requests durability through one committed write barrier.
	FlushEventName = "session/flush"
)

// LiveStore owns live Session membership and publication lifecycle.
// Persistence adapters observe append and flush events but never own lifecycle.
type LiveStore interface {
	plugin.Service
	Create(context.Context, *SessionID, CreateOptions) (Handle, error)
	Prepare(*SessionID, CreateOptions) (Context, error)
	Enter(Context) (Handle, error)
	Announce(context.Context, Context) error
	Flush(context.Context, Context) error
	Get(SessionID) (Context, bool)
	List() []Context
}

// Handle is the exact lifecycle capability returned by Store entry.
type Handle interface {
	Session() Context
	Release(context.Context) error
}

// eventPublisher is the typed publication port attached to one live Session.
// Its implementation owns delivery policy and post-commit failure reporting.
type eventPublisher interface {
	Created(context.Context, Context) error
	Appended(context.Context, Context, Event)
	Flush(context.Context, Context, WriteBarrier) error
	Disposed(context.Context, Context)
}

// Created is the vetoable publication edge after a Session enters the Store.
type Created struct {
	Conversation Context
}

func (Created) EventName() string { return CreatedEventName }
func (Created) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// Disposed announces post-removal cleanup as a contained notification.
type Disposed struct {
	Conversation Context
}

func (Disposed) EventName() string { return DisposedEventName }
func (Disposed) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryBestEffort
}

// EventAppended carries the exact Event that has already committed.
type EventAppended struct {
	Conversation Context
	Committed    Event
}

func (EventAppended) EventName() string { return AppendedEventName }
func (EventAppended) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryBestEffort
}

// FlushRequested asks durability observers to satisfy one committed prefix.
type FlushRequested struct {
	Conversation Context
	Barrier      WriteBarrier
}

func (FlushRequested) EventName() string { return FlushEventName }
func (FlushRequested) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryParallel
}

// TimeSource supplies timestamps without coupling Session to process globals.
type TimeSource interface {
	CurrentTime() time.Time
}

// PostCommitFailure identifies deferred work that failed after an Event commit.
type PostCommitFailure struct {
	SessionID SessionID
	Error     error
}

// PostCommitFailureReporter receives failures that cannot roll back a committed Event.
type PostCommitFailureReporter interface {
	ReportPostCommitFailure(PostCommitFailure)
}

// MemoryStoreOptions supplies technical collaborators without adding persistence policy.
type MemoryStoreOptions struct {
	TimeSource         TimeSource
	PostCommitFailures PostCommitFailureReporter
}
