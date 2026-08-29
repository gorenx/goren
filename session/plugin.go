package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/plugin"
)

// Plugin adapts the business Store to Runtime service binding and event
// publication. Per-Session state belongs to sessionLifecycle.
type Plugin struct {
	plugin.Base
	store         *memoryStore
	failureReport PostCommitFailureReporter
}

// NewPlugin constructs the canonical Session Plugin and its business Store.
func NewPlugin(options MemoryStoreOptions) (*Plugin, error) {
	if options.PostCommitFailures == nil {
		return nil, errors.New("session: post-commit failure reporter is required")
	}
	owner := &Plugin{
		failureReport: options.PostCommitFailures,
	}
	store, err := newMemoryStore(options.TimeSource, owner)
	if err != nil {
		return nil, err
	}
	owner.store = store
	return owner, nil
}

// Manifest publishes the independent Session Store Service.
func (owner *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[LiveStore](owner.store),
		},
	}
}

// Apply validates startup cancellation before Service publication.
func (*Plugin) Apply(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("session: Plugin Apply Context is nil")
	}
	return requestContext.Err()
}

// Dispose asks the business Store to release every live Session.
func (owner *Plugin) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	return owner.store.Close(context.WithoutCancel(closeContext))
}

func (owner *Plugin) Created(
	requestContext context.Context,
	conversation Context,
) error {
	return owner.publishSafely(
		requestContext,
		Created{
			Conversation: conversation,
		},
	)
}

func (owner *Plugin) Appended(
	requestContext context.Context,
	conversation Context,
	committed Event,
) {
	dispatchErr := owner.publishSafely(
		requestContext,
		EventAppended{
			Conversation: conversation,
			Committed:    cloneEvent(committed),
		},
	)
	if dispatchErr != nil {
		owner.reportPostCommitFailure(
			PostCommitFailure{
				SessionID: conversation.ID(),
				Error:     dispatchErr,
			},
		)
	}
}

func (owner *Plugin) Flush(
	requestContext context.Context,
	conversation Context,
	barrier WriteBarrier,
) error {
	return plugin.PublishEvent(
		requestContext,
		owner,
		FlushRequested{
			Conversation: conversation,
			Barrier:      barrier,
		},
	)
}

func (owner *Plugin) Disposed(
	requestContext context.Context,
	conversation Context,
) {
	_ = owner.publishSafely(
		requestContext,
		Disposed{
			Conversation: conversation,
		},
	)
}

func (owner *Plugin) reportPostCommitFailure(failure PostCommitFailure) {
	defer func() { _ = recover() }()
	owner.failureReport.ReportPostCommitFailure(failure)
}

func (owner *Plugin) publishSafely(
	requestContext context.Context,
	fact plugin.Event,
) (dispatchErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			dispatchErr = fmt.Errorf("session: listener panicked: %v", recovered)
		}
	}()
	return plugin.PublishEvent(requestContext, owner, fact)
}

var _ plugin.Plugin = (*Plugin)(nil)
var _ eventPublisher = (*Plugin)(nil)
