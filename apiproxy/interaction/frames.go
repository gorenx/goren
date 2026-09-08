package interaction

import (
	"errors"
	"fmt"

	"github.com/gorenx/goren/connection"
)

// ApprovalRequestedFrame asks the browser to resolve one approval.
type ApprovalRequestedFrame struct {
	SessionID  SessionID         `json:"sessionId"`
	ApprovalID ApprovalRequestID `json:"approvalId"`
	ToolName   string            `json:"toolName"`
	CallID     *string           `json:"callId,omitempty"`
	Reason     *string           `json:"reason,omitempty"`
}

func (ApprovalRequestedFrame) MuxFrame() {}

func (ApprovalRequestedFrame) Type() string { return "approval/requested" }

func (item ApprovalRequestedFrame) Validate() error {
	if err := validateSessionIdentifier(item.SessionID); err != nil {
		return err
	}
	if item.ApprovalID == "" {
		return errors.New("approvalId must be non-empty")
	}
	return nil
}

// ApprovalResolvedFrame publishes the authoritative approval result.
type ApprovalResolvedFrame struct {
	SessionID  SessionID         `json:"sessionId"`
	ApprovalID ApprovalRequestID `json:"approvalId"`
	Outcome    ApprovalOutcome   `json:"outcome"`
}

func (ApprovalResolvedFrame) MuxFrame() {}

func (ApprovalResolvedFrame) Type() string { return "approval/resolved" }

func (item ApprovalResolvedFrame) Validate() error {
	if err := validateSessionIdentifier(item.SessionID); err != nil {
		return err
	}
	if item.ApprovalID == "" {
		return errors.New("approvalId must be non-empty")
	}
	if item.Outcome != ApprovalAllowedOnce && item.Outcome != ApprovalRejected && item.Outcome != ApprovalCancelled && item.Outcome != ApprovalUnavailable {
		return errors.New("approval outcome is invalid")
	}
	return nil
}

// QuestionRequestedFrame asks the browser to answer one non-empty batch.
type QuestionRequestedFrame struct {
	SessionID SessionID             `json:"sessionId"`
	Questions []AskUserQuestionItem `json:"questions"`
}

func (QuestionRequestedFrame) MuxFrame() {}

func (QuestionRequestedFrame) Type() string { return "question/requested" }

func (item QuestionRequestedFrame) Validate() error {
	if err := validateSessionIdentifier(item.SessionID); err != nil {
		return err
	}
	return validateQuestionBatch(item.Questions)
}

// QuestionResolvedFrame publishes the authoritative question result.
type QuestionResolvedFrame struct {
	SessionID     SessionID          `json:"sessionId"`
	QuestionRPCID connection.RPCID   `json:"questionRpcId"`
	Outcome       QuestionResolution `json:"outcome"`
}

func (QuestionResolvedFrame) MuxFrame() {}

func (QuestionResolvedFrame) Type() string { return "question/resolved" }

func (item QuestionResolvedFrame) Validate() error {
	if err := validateSessionIdentifier(item.SessionID); err != nil {
		return err
	}
	if item.Outcome != QuestionAnswered && item.Outcome != QuestionCancelled {
		return errors.New("question outcome is invalid")
	}
	return nil
}

// ApprovalRequestID correlates an approval with its durable audit events.
type ApprovalRequestID string

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

// SessionID identifies the Session associated with a browser interaction.
type SessionID string

func validateSessionIdentifier(identifier SessionID) error {
	if identifier == "" {
		return errors.New("sessionId must be non-empty")
	}
	return nil
}
