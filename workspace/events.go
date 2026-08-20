package workspace

import (
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

// ChangedNotice carries one complete committed Workspace state.
type ChangedNotice struct {
	WorkspaceState WorkspaceState
}

func (ChangedNotice) EventName() string { return "workspace/changed" }

func (ChangedNotice) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryBestEffort
}

// RemovedNotice identifies one committed Workspace deletion.
type RemovedNotice struct {
	ID ID
}

func (RemovedNotice) EventName() string { return "workspace/removed" }

func (RemovedNotice) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryBestEffort
}

// OrderChangedNotice carries the complete committed display order.
type OrderChangedNotice struct {
	WorkspaceIDs []ID
}

func (OrderChangedNotice) EventName() string { return "workspace/order-changed" }

func (OrderChangedNotice) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryBestEffort
}

// ArchivedSessionsChangedNotice carries the complete committed archive set.
type ArchivedSessionsChangedNotice struct {
	SessionIDs []session.SessionID
}

func (ArchivedSessionsChangedNotice) EventName() string {
	return "workspace/archived-sessions-changed"
}

func (ArchivedSessionsChangedNotice) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryBestEffort
}
