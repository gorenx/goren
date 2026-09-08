package agent

import (
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

// CreatedTopic carries CreatedNotice values.
const CreatedTopic = "agent/created"

// CreatedNotice reports that an Agent is ready to process work.
type CreatedNotice struct {
	AgentID session.SessionID
}

// DisposedTopic carries DisposedNotice values.
const DisposedTopic = "agent/disposed"

// DisposedNotice reports completed Agent retirement.
type DisposedNotice struct {
	AgentID session.SessionID
}

// StatusTopic carries StatusNotice values.
const StatusTopic = "agent/status"

// StatusNotice reports a business status change.
type StatusNotice struct {
	AgentID session.SessionID
	Status  Status
}

// InboxInsertedTopic carries InboxInsertedNotice values.
const InboxInsertedTopic = "agent/inbox/inserted"

// InboxInsertedNotice reports a committed Inbox insertion.
type InboxInsertedNotice struct {
	AgentID session.SessionID
	Message agentmessage.UserMessage
}

// InboxClaimedTopic carries InboxClaimedNotice values.
const InboxClaimedTopic = "agent/inbox/claimed"

// InboxClaimedNotice reports committed input claimed for one turn.
type InboxClaimedNotice struct {
	AgentID session.SessionID
	Message agentmessage.UserMessage
	Turn    int64
}

// InboxDiscardedTopic carries InboxDiscardedNotice values.
const InboxDiscardedTopic = "agent/inbox/discarded"

// InboxDiscardedNotice reports committed removal without execution.
type InboxDiscardedNotice struct {
	AgentID session.SessionID
	Message agentmessage.UserMessage
}

// SessionStartTopic carries SessionStartNotice values.
const SessionStartTopic = "agent/session-start"

// SessionStartNotice reports the first Session driving boundary.
type SessionStartNotice struct {
	AgentID session.SessionID
	Source  SessionStartSource
}

// TurnStoppingTopic carries TurnStoppingNotice values.
const TurnStoppingTopic = "agent/turn-stopping"

// TurnStoppingNotice reports the turn stopping boundary without admitting new work.
type TurnStoppingNotice struct {
	AgentID session.SessionID
	Turn    int64
}

// ErrorTopic carries ErrorNotice values.
const ErrorTopic = "agent/error"

// ErrorNotice reports an Agent execution failure while preserving its cause.
type ErrorNotice struct {
	AgentID session.SessionID
	Turn    int64
	Step    int64
	Err     error
}

// SessionStartSource classifies why an Agent's Session lifecycle began.
type SessionStartSource string

const (
	// SessionStartup means a new Session was created.
	SessionStartup SessionStartSource = "startup"
	// SessionResume means durable Session state was restored.
	SessionResume SessionStartSource = "resume"
	// SessionClear means the active Session was cleared.
	SessionClear SessionStartSource = "clear"
	// SessionCompact means compaction began a replacement Session view.
	SessionCompact SessionStartSource = "compact"
)
