package agentactor

import goakt "github.com/tochemey/goakt/v4/actor"

// BeginMaintenance reserves an idle Agent for work owned by another component.
// RequestID must be unique per invocation. Worker receives cancellation requests.
type BeginMaintenance struct {
	CreationID string
	RequestID  string
	Worker     *goakt.GrainIdentity
}

// MaintenanceAccepted confirms exclusive admission before any work may begin.
type MaintenanceAccepted struct {
	Err error
}

// FinishMaintenance releases only the matching, fully settled invocation.
type FinishMaintenance struct {
	CreationID string
	RequestID  string
}

// MaintenanceFinished confirms that the Agent has released maintenance ownership.
type MaintenanceFinished struct {
	Err error
}

// CancelMaintenance delivers the Agent's first cancellation intent to the worker.
type CancelMaintenance struct {
	RequestID string
	Cause     error
}

// MaintenanceCancelled acknowledges intent delivery, not completion of the work.
type MaintenanceCancelled struct {
	Err error
}
