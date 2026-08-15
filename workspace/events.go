package workspace

import (
	"context"
	"errors"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

// ChangedNotice carries one complete committed Workspace state.
type ChangedNotice struct {
	WorkspaceState WorkspaceState
}

// RemovedNotice identifies one committed Workspace deletion.
type RemovedNotice struct {
	ID ID
}

// OrderChangedNotice carries the complete committed display order.
type OrderChangedNotice struct {
	WorkspaceIDs []ID
}

// ArchivedSessionsChangedNotice carries the complete committed archive set.
type ArchivedSessionsChangedNotice struct {
	SessionIDs []session.SessionID
}

var (
	changedTopic = plugin.DefineEvent[ChangedNotice, struct{}]("workspace/changed", plugin.ModeEmit)
	removedTopic = plugin.DefineEvent[RemovedNotice, struct{}]("workspace/removed", plugin.ModeEmit)
	orderTopic   = plugin.DefineEvent[OrderChangedNotice, struct{}]("workspace/order-changed", plugin.ModeEmit)
	archiveTopic = plugin.DefineEvent[ArchivedSessionsChangedNotice, struct{}]("workspace/archived-sessions-changed", plugin.ModeEmit)
)

// OnChanged observes one complete committed Workspace upsert.
func OnChanged(pluginScope *plugin.Scope, callback func(context.Context, WorkspaceState) error) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("workspace: changed listener is nil")
	}
	return plugin.OnNotify(pluginScope, changedTopic, func(requestContext context.Context, notice ChangedNotice) error {
		return callback(requestContext, cloneState(notice.WorkspaceState))
	})
}

// OnRemoved observes a committed Workspace deletion.
func OnRemoved(pluginScope *plugin.Scope, callback func(context.Context, ID) error) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("workspace: removed listener is nil")
	}
	return plugin.OnNotify(pluginScope, removedTopic, func(requestContext context.Context, notice RemovedNotice) error {
		return callback(requestContext, notice.ID)
	})
}

// OnOrderChanged observes replacement of the durable Workspace order.
func OnOrderChanged(pluginScope *plugin.Scope, callback func(context.Context, []ID) error) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("workspace: order listener is nil")
	}
	return plugin.OnNotify(pluginScope, orderTopic, func(requestContext context.Context, notice OrderChangedNotice) error {
		return callback(requestContext, append([]ID(nil), notice.WorkspaceIDs...))
	})
}

// OnArchivedSessionsChanged observes replacement of the durable archive set.
func OnArchivedSessionsChanged(
	pluginScope *plugin.Scope,
	callback func(context.Context, []session.SessionID) error,
) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("workspace: archive listener is nil")
	}
	return plugin.OnNotify(pluginScope, archiveTopic,
		func(requestContext context.Context, notice ArchivedSessionsChangedNotice) error {
			return callback(requestContext, append([]session.SessionID(nil), notice.SessionIDs...))
		})
}
