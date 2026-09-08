package agentactor

// PrepareClose stops new input admission and checks whether accepted work has finished.
// A non-nil Prepared.Err means resources must remain available for completion.
type PrepareClose struct {
	CreationID string
}

// Prepared reports whether accepted work has finished, not resource shutdown.
type Prepared struct {
	Err error
}

// CloseResources releases this Agent's resources after preparation and after
// the Registry has confirmed that its children have closed.
type CloseResources struct {
	CreationID string
}

// Closed confirms resource shutdown only when Err is nil. The Registry still
// owns final Agent actor deactivation and release of the Agent reservation.
type Closed struct {
	Err error
}
