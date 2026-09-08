package agent

import "github.com/gorenx/goren/session"

// CreatingTopic carries internal initialization triggers, before Agent readiness.
// It does not replace the externally observable CreatedNotice.
const CreatingTopic = "agent/creating"

// CreatingNotice identifies a reserved Agent whose component inputs are already registered.
// It contains no live Agent reference, component config, or framework address.
type CreatingNotice struct {
	AgentID session.SessionID
	// CreationID identifies the reserved creation; event retries keep this value.
	CreationID string
}
