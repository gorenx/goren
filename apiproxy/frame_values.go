package apiproxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// SessionID is the wire identity of one Harness session.
type SessionID string

// ApprovalRequestID correlates an approval with its durable audit events.
type ApprovalRequestID string

// MessageID identifies one immutable message or transient queue occurrence.
type MessageID string

// WorkspaceID identifies one workspace registry entry.
type WorkspaceID string

// JobID identifies one process-local background job.
type JobID string

// ApprovalOutcome is the complete host-side approval resolution vocabulary.
type ApprovalOutcome string

const (
	ApprovalAllowedOnce ApprovalOutcome = "allowed-once"
	ApprovalRejected    ApprovalOutcome = "rejected"
	ApprovalCancelled   ApprovalOutcome = "cancelled"
	ApprovalUnavailable ApprovalOutcome = "unavailable"
)

// QuestionResolution is the terminal state of one question request.
type QuestionResolution string

const (
	QuestionAnswered  QuestionResolution = "answered"
	QuestionCancelled QuestionResolution = "cancelled"
)

// QueuePlacement is the Agent-resolved transient inbox placement.
type QueuePlacement string

const (
	QueueQueued   QueuePlacement = "queued"
	QueueSteering QueuePlacement = "steering"
	QueueContext  QueuePlacement = "context"
)

// MessageRole is the provider-neutral conversation role.
type MessageRole string

const (
	RoleSystem    MessageRole = "system"
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
)

// JobStatus is the browser-visible background job lifecycle state.
type JobStatus string

const (
	JobRunning   JobStatus = "running"
	JobStopping  JobStatus = "stopping"
	JobCompleted JobStatus = "completed"
	JobKilled    JobStatus = "killed"
	JobFailed    JobStatus = "failed"
)

// QuestionOption is one selectable answer shown to the user.
type QuestionOption struct {
	Label       string  `json:"label"`
	Description *string `json:"description,omitempty"`
}

// QuestionIntent carries optional presentation intent for a question.
type QuestionIntent struct {
	Kind    string `json:"kind"`
	Approve string `json:"approve"`
}

// AskUserQuestionItem is one member of an answerable question batch.
type AskUserQuestionItem struct {
	ID          string            `json:"id"`
	Question    string            `json:"question"`
	Header      *string           `json:"header,omitempty"`
	Detail      *string           `json:"detail,omitempty"`
	Options     *[]QuestionOption `json:"options,omitempty"`
	MultiSelect *bool             `json:"multiSelect,omitempty"`
	Intent      *QuestionIntent   `json:"intent,omitempty"`
}

// SessionEvent preserves the strict session-log envelope while leaving data
// and surfaceOp with their owner-defined JSON shapes.
type SessionEvent struct {
	Type            string          `json:"type"`
	Seq             int64           `json:"seq"`
	Time            int64           `json:"time"`
	Data            json.RawMessage `json:"data"`
	SourceEventSeqs *[]int64        `json:"sourceEventSeqs,omitempty"`
	SurfaceOp       json.RawMessage `json:"surfaceOp,omitempty"`
	Ignorable       bool            `json:"ignorable,omitempty"`
}

// ToolEventView is the call/result render-intent wrapper. View stays wide
// because the registered Tool presenter owns its card-specific fields.
type ToolEventView struct {
	For  string          `json:"for"`
	View json.RawMessage `json:"view"`
}

// QueuedMessage is the browser-safe message projection carried by a queue
// snapshot. Content blocks and source remain merge-extensible JSON objects.
type QueuedMessage struct {
	ID      MessageID         `json:"id"`
	Role    MessageRole       `json:"role"`
	Content []json.RawMessage `json:"content"`
	Source  json.RawMessage   `json:"source"`
}

// QueuedInboxItem is one occurrence in the authoritative queue snapshot.
type QueuedInboxItem struct {
	ID        MessageID      `json:"id"`
	Placement QueuePlacement `json:"placement"`
	Message   QueuedMessage  `json:"message"`
}

// JobView is the browser-safe subset of a background job record.
type JobView struct {
	ID         JobID     `json:"id"`
	Kind       string    `json:"kind"`
	Label      string    `json:"label"`
	Status     JobStatus `json:"status"`
	Detail     *string   `json:"detail,omitempty"`
	StartedAt  int64     `json:"startedAt"`
	FinishedAt *int64    `json:"finishedAt,omitempty"`
}

// WorkspaceView is the browser-visible projection of a workspace record.
type WorkspaceView struct {
	WorkspaceID WorkspaceID `json:"workspaceId"`
	Path        string      `json:"path"`
	Title       string      `json:"title"`
	SessionIDs  []SessionID `json:"sessionIds"`
	CreatedAt   string      `json:"createdAt"`
	UpdatedAt   string      `json:"updatedAt"`
}

func validateSessionIdentifier(identifier SessionID) error {
	if identifier == "" {
		return errors.New("sessionId must be non-empty")
	}
	return nil
}

func validateSessionEvent(record SessionEvent) error {
	if record.Seq < 0 {
		return errors.New("session event seq must be non-negative")
	}
	if record.Data != nil && !json.Valid(record.Data) {
		return errors.New("session event data must be JSON")
	}
	if record.SurfaceOp != nil && !json.Valid(record.SurfaceOp) {
		return errors.New("session event surfaceOp must be JSON")
	}
	return nil
}

func validateToolEventView(presentation *ToolEventView) error {
	if presentation == nil {
		return nil
	}
	if presentation.For != "call" && presentation.For != "result" {
		return errors.New("tool event view for must be call or result")
	}
	return validateJSONObjectWithString("tool event view", presentation.View, "card")
}

func validateQuestionBatch(questions []AskUserQuestionItem) error {
	if len(questions) == 0 {
		return errors.New("question batch must be non-empty")
	}
	for index, question := range questions {
		if question.Intent == nil {
			continue
		}
		if question.Intent.Kind != "plan-review" {
			return fmt.Errorf("question %d intent kind must be plan-review", index)
		}
	}
	return nil
}

func validateQueueItems(items []QueuedInboxItem) error {
	if items == nil {
		return errors.New("queue items must be an array")
	}
	for index, queued := range items {
		if queued.ID == "" {
			return fmt.Errorf("queue item %d id must be non-empty", index)
		}
		if queued.Placement != QueueQueued && queued.Placement != QueueSteering && queued.Placement != QueueContext {
			return fmt.Errorf("queue item %d has invalid placement", index)
		}
		if err := validateQueuedMessage(queued.Message); err != nil {
			return fmt.Errorf("queue item %d: %w", index, err)
		}
	}
	return nil
}

func validateQueuedMessage(message QueuedMessage) error {
	if message.ID == "" {
		return errors.New("message id must be non-empty")
	}
	if message.Role != RoleSystem && message.Role != RoleUser && message.Role != RoleAssistant {
		return errors.New("message role is invalid")
	}
	if message.Content == nil {
		return errors.New("message content must be an array")
	}
	for index, block := range message.Content {
		if err := validateJSONObjectWithString("content block", block, "type"); err != nil {
			return fmt.Errorf("content block %d: %w", index, err)
		}
	}
	return validateJSONObjectWithString("message source", message.Source, "kind")
}

func validateJobs(tasks []JobView) error {
	if tasks == nil {
		return errors.New("jobs must be an array")
	}
	for index, task := range tasks {
		if task.ID == "" || task.Kind == "" || task.Label == "" {
			return fmt.Errorf("job %d id, kind, and label must be non-empty", index)
		}
		if task.Status != JobRunning && task.Status != JobStopping && task.Status != JobCompleted && task.Status != JobKilled && task.Status != JobFailed {
			return fmt.Errorf("job %d status is invalid", index)
		}
		if task.StartedAt < 0 || (task.FinishedAt != nil && *task.FinishedAt < 0) {
			return fmt.Errorf("job %d timestamps must be non-negative", index)
		}
	}
	return nil
}

func validateWorkspace(snapshot WorkspaceView) error {
	if snapshot.WorkspaceID == "" {
		return errors.New("workspaceId must be non-empty")
	}
	if snapshot.SessionIDs == nil {
		return errors.New("workspace sessionIds must be an array")
	}
	for _, identifier := range snapshot.SessionIDs {
		if err := validateSessionIdentifier(identifier); err != nil {
			return err
		}
	}
	return nil
}

func validateJSONObjectWithString(label string, raw json.RawMessage, field string) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return fmt.Errorf("%s must be a JSON object", label)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil || fields == nil {
		return fmt.Errorf("%s must be a JSON object", label)
	}
	var value string
	if err := json.Unmarshal(fields[field], &value); err != nil {
		return fmt.Errorf("%s %s must be a string", label, field)
	}
	return nil
}

func cloneStringPointer(source *string) *string {
	if source == nil {
		return nil
	}
	copyValue := *source
	return &copyValue
}
