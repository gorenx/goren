package sessionevents

import (
	"encoding/json"
	"errors"
)

// EventFrame carries one immutable session-log event.
type EventFrame struct {
	SessionID SessionID `json:"sessionId"`
	Event     LogEvent  `json:"event"`
	View      *ToolView `json:"view,omitempty"`
}

func (EventFrame) MuxFrame() {}

func (EventFrame) Type() string { return "session/event" }

func (item EventFrame) Validate() error {
	if err := ValidateSessionID(item.SessionID); err != nil {
		return err
	}
	if err := validateLogEvent(item.Event); err != nil {
		return err
	}
	return validateToolView(item.View)
}

// SubscribedFrame establishes the current session sequence baseline.
type SubscribedFrame struct {
	SessionID SessionID `json:"sessionId"`
	LastSeq   int64     `json:"lastSeq"`
}

func (SubscribedFrame) MuxFrame() {}

func (SubscribedFrame) Type() string { return "session/subscribed" }

func (item SubscribedFrame) Validate() error {
	return ValidateSessionID(item.SessionID)
}

// QueueFrame replaces the browser's transient inbox snapshot.
type QueueFrame struct {
	SessionID SessionID   `json:"sessionId"`
	Items     []QueueItem `json:"items"`
}

func (QueueFrame) MuxFrame() {}

func (QueueFrame) Type() string { return "session/queue" }

func (item QueueFrame) Validate() error {
	if err := ValidateSessionID(item.SessionID); err != nil {
		return err
	}
	return validateQueueItems(item.Items)
}

// JobsFrame replaces the browser's background-job snapshot.
type JobsFrame struct {
	SessionID SessionID `json:"sessionId"`
	Jobs      []Job     `json:"jobs"`
}

func (JobsFrame) MuxFrame() {}

func (JobsFrame) Type() string { return "session/jobs" }

func (item JobsFrame) Validate() error {
	if err := ValidateSessionID(item.SessionID); err != nil {
		return err
	}
	return validateJobs(item.Jobs)
}

// ProjectionFrame publishes one validated projection unit value.
type ProjectionFrame struct {
	SessionID SessionID       `json:"sessionId"`
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	Seq       int64           `json:"seq"`
}

func (ProjectionFrame) MuxFrame() {}

func (ProjectionFrame) Type() string { return "session/projection" }

func (item ProjectionFrame) Validate() error {
	if err := ValidateSessionID(item.SessionID); err != nil {
		return err
	}
	if item.Key == "" {
		return errors.New("projection key must be non-empty")
	}
	if item.Seq < 0 {
		return errors.New("projection seq must be non-negative")
	}
	if len(item.Value) == 0 || !json.Valid(item.Value) {
		return errors.New("projection value must be JSON")
	}
	return nil
}

// AddedFrame announces a newly attached session.
type AddedFrame struct {
	SessionID       SessionID `json:"sessionId"`
	Blank           bool      `json:"blank"`
	ParentSessionID SessionID `json:"parentSessionId,omitempty"`
	Origin          string    `json:"origin,omitempty"`
	CWD             *string   `json:"cwd,omitempty"`
	AgentPreset     *string   `json:"agentPreset,omitempty"`
}

func (AddedFrame) HostFrame() {}

func (AddedFrame) Type() string { return "host/session-added" }

func (item AddedFrame) Validate() error {
	if err := ValidateSessionID(item.SessionID); err != nil {
		return err
	}
	if item.ParentSessionID != "" {
		if err := ValidateSessionID(item.ParentSessionID); err != nil {
			return err
		}
	}
	if item.Origin != "" && item.Origin != "subagent" {
		return errors.New("session origin must be subagent")
	}
	return nil
}

// RemovedFrame announces detachment of one session.
type RemovedFrame struct {
	SessionID SessionID `json:"sessionId"`
}

func (RemovedFrame) HostFrame() {}

func (RemovedFrame) Type() string { return "host/session-removed" }

func (item RemovedFrame) Validate() error {
	return ValidateSessionID(item.SessionID)
}

// StatusFrame publishes the running state of one session.
type StatusFrame struct {
	SessionID SessionID `json:"sessionId"`
	Running   bool      `json:"running"`
}

func (StatusFrame) HostFrame() {}

func (StatusFrame) Type() string { return "host/session-status" }

func (item StatusFrame) Validate() error {
	return ValidateSessionID(item.SessionID)
}

// AgentErrorFrame reports a live Agent failure without turn position.
type AgentErrorFrame struct {
	SessionID SessionID `json:"sessionId"`
	Message   string    `json:"message"`
}

func (AgentErrorFrame) HostFrame() {}

func (AgentErrorFrame) Type() string { return "host/agent-error" }

func (item AgentErrorFrame) Validate() error {
	return ValidateSessionID(item.SessionID)
}

// SessionID is the wire identity of one Harness session.
type SessionID string

func ValidateSessionID(identifier SessionID) error {
	if identifier == "" {
		return errors.New("sessionId must be non-empty")
	}
	return nil
}
