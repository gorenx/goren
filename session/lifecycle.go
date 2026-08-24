package session

import (
	"context"
	"time"

	"github.com/gorenx/goren/plugin"
)

const (
	PluginName        = "@deepseek-ai/dsh-session"
	CreatedEventName  = "session/created"
	DisposedEventName = "session/disposed"
	AppendedEventName = "session/event"
	FlushEventName    = "session/flush"
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

// Handle owns one live Session membership.
type Handle interface {
	Session() Context
	Release(context.Context) error
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
