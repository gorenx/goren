package sessionactor

import "github.com/gorenx/goren/session"

// CommitNotice carries one detached event after its Session batch commits in memory.
// It is not a persistence acknowledgement. Consumers match both identities.
// For example, a notice with Event.Type == session.RequestHeaderEventName carries
// a session.RequestHeaderSnapshot in Event.Data; it does not carry session.Header.
// Event.Seq locates that fact in the same log as turn/start and step/start.
// Consumers reconstruct missed facts from the Session log, not from this notice.
// Receipt alone does not mean another subscriber has applied the event.
type CommitNotice struct {
	SessionID  session.SessionID
	CreationID string
	Event      session.Event
}

// Clone detaches event data before transferring ownership to event consumers.
func (source CommitNotice) Clone() CommitNotice {
	source.Event = source.Event.Clone()
	return source
}

// ClosedTopic carries notifications after business drain and framework deactivation.
const ClosedTopic = "session/closed"

// ClosedNotice reports completed Session shutdown, not subscriber acknowledgement.
// The barrier identifies the final durable log prefix.
type ClosedNotice struct {
	SessionID  session.SessionID
	CreationID string
	Barrier    session.WriteBarrier
}
