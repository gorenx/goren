package apiproxy

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/connection"
)

// MuxFrame is the closed payload union for the all-session mux stream.
type MuxFrame interface {
	frameContract
	muxFrame()
}

// SessionEventFrame carries one immutable session-log event.
type SessionEventFrame struct {
	SessionID SessionID      `json:"sessionId"`
	Event     SessionEvent   `json:"event"`
	View      *ToolEventView `json:"view,omitempty"`
}

func (SessionEventFrame) muxFrame()         {}
func (SessionEventFrame) frameType() string { return "session/event" }
func (item SessionEventFrame) validate() error {
	if err := validateSessionIdentifier(item.SessionID); err != nil {
		return err
	}
	if err := validateSessionEvent(item.Event); err != nil {
		return err
	}
	return validateToolEventView(item.View)
}

// SessionSubscribedFrame establishes the current session sequence baseline.
type SessionSubscribedFrame struct {
	SessionID SessionID `json:"sessionId"`
	LastSeq   int64     `json:"lastSeq"`
}

func (SessionSubscribedFrame) muxFrame()         {}
func (SessionSubscribedFrame) frameType() string { return "session/subscribed" }
func (item SessionSubscribedFrame) validate() error {
	return validateSessionIdentifier(item.SessionID)
}

// ApprovalRequestedFrame asks the browser to resolve one approval.
type ApprovalRequestedFrame struct {
	SessionID  SessionID         `json:"sessionId"`
	ApprovalID ApprovalRequestID `json:"approvalId"`
	ToolName   string            `json:"toolName"`
	CallID     *string           `json:"callId,omitempty"`
	Reason     *string           `json:"reason,omitempty"`
}

func (ApprovalRequestedFrame) muxFrame()         {}
func (ApprovalRequestedFrame) frameType() string { return "approval/requested" }
func (item ApprovalRequestedFrame) validate() error {
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

func (ApprovalResolvedFrame) muxFrame()         {}
func (ApprovalResolvedFrame) frameType() string { return "approval/resolved" }
func (item ApprovalResolvedFrame) validate() error {
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

func (QuestionRequestedFrame) muxFrame()         {}
func (QuestionRequestedFrame) frameType() string { return "question/requested" }
func (item QuestionRequestedFrame) validate() error {
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

func (QuestionResolvedFrame) muxFrame()         {}
func (QuestionResolvedFrame) frameType() string { return "question/resolved" }
func (item QuestionResolvedFrame) validate() error {
	if err := validateSessionIdentifier(item.SessionID); err != nil {
		return err
	}
	if item.Outcome != QuestionAnswered && item.Outcome != QuestionCancelled {
		return errors.New("question outcome is invalid")
	}
	return nil
}

// SessionQueueFrame replaces the browser's transient inbox snapshot.
type SessionQueueFrame struct {
	SessionID SessionID         `json:"sessionId"`
	Items     []QueuedInboxItem `json:"items"`
}

func (SessionQueueFrame) muxFrame()         {}
func (SessionQueueFrame) frameType() string { return "session/queue" }
func (item SessionQueueFrame) validate() error {
	if err := validateSessionIdentifier(item.SessionID); err != nil {
		return err
	}
	return validateQueueItems(item.Items)
}

// SessionJobsFrame replaces the browser's background-job snapshot.
type SessionJobsFrame struct {
	SessionID SessionID `json:"sessionId"`
	Jobs      []JobView `json:"jobs"`
}

func (SessionJobsFrame) muxFrame()         {}
func (SessionJobsFrame) frameType() string { return "session/jobs" }
func (item SessionJobsFrame) validate() error {
	if err := validateSessionIdentifier(item.SessionID); err != nil {
		return err
	}
	return validateJobs(item.Jobs)
}

// SessionProjectionFrame publishes one validated projection unit value.
type SessionProjectionFrame struct {
	SessionID SessionID       `json:"sessionId"`
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	Seq       int64           `json:"seq"`
}

func (SessionProjectionFrame) muxFrame()         {}
func (SessionProjectionFrame) frameType() string { return "session/projection" }
func (item SessionProjectionFrame) validate() error {
	if err := validateSessionIdentifier(item.SessionID); err != nil {
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

// StreamErrorFrame reports a terminal stream failure. It belongs to both
// logical frame unions.
type StreamErrorFrame struct {
	Error connection.RPCError `json:"error"`
}

func (StreamErrorFrame) muxFrame()         {}
func (StreamErrorFrame) hostFrame()        {}
func (StreamErrorFrame) frameType() string { return "stream/error" }
func (item StreamErrorFrame) validate() error {
	if !item.Error.Valid() {
		return errors.New("stream error does not match the RPC error contract")
	}
	return nil
}

// EncodeMuxFrame validates and serializes one mux payload with its canonical
// type discriminant.
func EncodeMuxFrame(payload MuxFrame) (json.RawMessage, error) {
	encoded, err := encodeFrame(payload)
	if err != nil {
		return nil, fmt.Errorf("apiproxy: encode mux frame: %w", err)
	}
	return encoded, nil
}
