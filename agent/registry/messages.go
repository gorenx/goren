// Package registry owns the Agent Registry framework boundary.
package registry

import "github.com/gorenx/goren/session"

// Creation identifies one reserved Agent lifetime, not only its reusable ID.
type Creation struct {
	AgentID    session.SessionID
	CreationID string
}

// Reserve admits one creation under an optional current parent creation.
type Reserve struct {
	Creation Creation
	Parent   *Creation
}

// CheckAdmission validates both creation identity and ancestor admission.
type CheckAdmission struct {
	Creation Creation
}

// BeginClose stops admission without claiming that any resource has closed.
type BeginClose struct {
	Creation Creation
}

// CloseAccepted reports close admission and the remaining direct children in
// Agent ID order. Children includes reservations still being initialized.
// It does not confirm that the Agent or any child has released its resources.
type CloseAccepted struct {
	Children []Creation
	Err      error
}

// ReleaseReservation removes a reservation after its lifecycle owner confirms
// that no owned resources remain. It is not an external close operation.
type ReleaseReservation struct {
	Creation Creation
}

// Completion acknowledges the requested registry operation only.
type Completion struct {
	Err error
}

// CloseAgent closes a creation and its descendants before releasing its identity.
type CloseAgent struct {
	Creation Creation
}
