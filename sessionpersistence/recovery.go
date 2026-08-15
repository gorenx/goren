package sessionpersistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

const (
	toolNotStartedCode     = "TOOL_NOT_STARTED"
	toolOutcomeUnknownCode = "TOOL_OUTCOME_UNKNOWN"
)

type pendingToolCall struct {
	callID  llm.CallID
	step    int64
	callSeq *int64
}

type recoveryToolSource struct {
	Kind   string     `json:"kind"`
	CallID llm.CallID `json:"callId"`
}

type recoveryToolResultBlock struct {
	Type       string          `json:"type"`
	ToolCallID llm.CallID      `json:"toolCallId"`
	IsError    bool            `json:"isError"`
	Content    []llm.TextBlock `json:"content"`
}

type recoveryToolMessage struct {
	ID      llm.MessageID             `json:"id"`
	Role    llm.MessageRole           `json:"role"`
	Source  recoveryToolSource        `json:"source"`
	Content []recoveryToolResultBlock `json:"content"`
}

type recoveryToolPayload struct {
	Turn    int64                 `json:"turn"`
	Step    int64                 `json:"step"`
	Message recoveryToolMessage   `json:"message"`
	Error   session.ToolErrorInfo `json:"error"`
}

func inspectStored(metadata session.Header, entries []session.Event) (Inspection, error) {
	if metadata.Version != session.FormatVersion {
		direction := "older than the supported"
		if metadata.Version > session.FormatVersion {
			direction = "newer than the supported"
		}
		return Inspection{}, fmt.Errorf(
			"session %q uses log format v%d, %s v%d",
			metadata.ID, metadata.Version, direction, session.FormatVersion,
		)
	}
	for _, entry := range entries {
		if !session.IsKnownEventType(entry.Type) && !entry.Ignorable {
			return Inspection{}, &UnsupportedFormatError{
				ID: metadata.ID,
				Reason: fmt.Sprintf(
					"session %q contains event type %q at seq %d unknown to this harness and not marked ignorable",
					metadata.ID, entry.Type, entry.Seq,
				),
			}
		}
	}
	conversation, err := session.New(metadata.ID, session.CreateOptions{
		Seed: entries,
		Metadata: session.Metadata{
			CreatedAt:       int64Pointer(metadata.CreatedAt),
			CWD:             cloneTextPointer(metadata.CWD),
			ParentSession:   cloneSessionPointer(metadata.ParentSession),
			SeedLength:      cloneInt64Pointer(metadata.SeedLength),
			Origin:          metadata.Origin,
			DelegationDepth: cloneInt64Pointer(metadata.DelegationDepth),
			AgentPreset:     cloneTextPointer(metadata.AgentPreset),
		},
	})
	if err != nil {
		return Inspection{}, err
	}
	if _, err := conversation.DeriveMessages(); err != nil {
		return Inspection{}, err
	}
	if _, _, err := conversation.RequestHeaderValue(); err != nil {
		return Inspection{}, err
	}
	if _, _, err := conversation.RequestContextValue(); err != nil {
		return Inspection{}, err
	}
	return Inspection{Header: conversation.Header(), Events: snapshotEvents(entries)}, nil
}

func interruptedTurnClosers(entries []session.Event) ([]session.Event, error) {
	var openTurn *int64
	var openStep *int64
	pending := make([]pendingToolCall, 0)
	positions := make(map[llm.CallID]int)
	for _, entry := range entries {
		switch entry.Type {
		case session.TurnStartEventName:
			var payload struct {
				Turn int64 `json:"turn"`
			}
			if err := decodeExact(entry.Data, &payload); err != nil || payload.Turn < 1 {
				return nil, fmt.Errorf("invalid turn/start at seq %d", entry.Seq)
			}
			openTurn = int64Pointer(payload.Turn)
			openStep = nil
			pending = pending[:0]
			clear(positions)
		case session.TurnEndEventName:
			if err := validateTurnEnd(entry); err != nil {
				return nil, err
			}
			openTurn = nil
			openStep = nil
			pending = pending[:0]
			clear(positions)
		case session.StepStartEventName:
			position, err := decodeStepPosition(entry)
			if err != nil {
				return nil, err
			}
			openStep = int64Pointer(position.Step)
		case session.StepEndEventName:
			if _, err := decodeStepPosition(entry); err != nil {
				return nil, err
			}
			openStep = nil
			pending = pending[:0]
			clear(positions)
		case session.AssistantMessageEventName:
			calls, step, err := assistantToolCalls(entry)
			if err != nil {
				return nil, err
			}
			for _, callID := range calls {
				positions[callID] = len(pending)
				pending = append(pending, pendingToolCall{callID: callID, step: step})
			}
		case session.ToolCallEventName:
			var payload struct {
				CallID llm.CallID `json:"callId"`
			}
			if err := json.Unmarshal(entry.Data, &payload); err != nil || payload.CallID == "" {
				return nil, fmt.Errorf("invalid tool/call at seq %d", entry.Seq)
			}
			if index, found := positions[payload.CallID]; found {
				sequence := entry.Seq
				pending[index].callSeq = &sequence
			}
		case session.ToolResultEventName:
			callID, err := toolResultCallID(entry)
			if err != nil {
				return nil, err
			}
			index, found := positions[callID]
			if found {
				pending = append(pending[:index], pending[index+1:]...)
				clear(positions)
				for pendingIndex := range pending {
					positions[pending[pendingIndex].callID] = pendingIndex
				}
			}
		}
	}
	if openTurn == nil || len(entries) == 0 {
		return nil, nil
	}
	last := entries[len(entries)-1]
	nextSeq := last.Seq + 1
	closers := make([]session.Event, 0, len(pending)+2)
	for _, call := range pending {
		started := call.callSeq != nil
		message := "The tool call was interrupted before the Harness recorded it as started. Retry it if it is still needed."
		errorName := "ToolNotStartedError"
		errorCode := toolNotStartedCode
		if started {
			message = "The tool call was interrupted after it was recorded, but no result was durably recorded. Its outcome is unknown. Decide whether to retry from the tool semantics: retry only if the operation is read-only or idempotent; if it may have side effects, first verify external state or ask the user. Do not retry blindly."
			errorName = "ToolOutcomeUnknownError"
			errorCode = toolOutcomeUnknownCode
		}
		payload := recoveryToolPayload{
			Turn: *openTurn,
			Step: call.step,
			Message: recoveryToolMessage{
				ID:     llm.MessageID(fmt.Sprintf("interrupted-tool-result-%s-%d", call.callID, nextSeq)),
				Role:   llm.RoleUser,
				Source: recoveryToolSource{Kind: "tool", CallID: call.callID},
				Content: []recoveryToolResultBlock{{
					Type: "tool-result", ToolCallID: call.callID, IsError: true,
					Content: []llm.TextBlock{llm.NewTextBlock(message)},
				}},
			},
			Error: session.ToolErrorInfo{Name: errorName, Code: errorCode},
		}
		rawValue, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		operation := session.SurfaceAppend()
		closer := session.Event{
			Type: session.ToolResultEventName, Seq: nextSeq, Time: last.Time,
			Data: rawValue, SurfaceOp: &operation,
		}
		if call.callSeq != nil {
			sources := []int64{*call.callSeq}
			closer.SourceEventSeqs = &sources
		}
		closers = append(closers, closer)
		nextSeq++
	}
	if openStep != nil {
		rawValue, err := json.Marshal(session.StepPosition{Turn: *openTurn, Step: *openStep})
		if err != nil {
			return nil, err
		}
		closers = append(closers, session.Event{
			Type: session.StepEndEventName, Seq: nextSeq, Time: last.Time, Data: rawValue,
		})
		nextSeq++
	}
	rawValue, err := json.Marshal(session.TurnEnd{Turn: *openTurn, Reason: session.TurnInterrupted{}})
	if err != nil {
		return nil, err
	}
	closers = append(closers, session.Event{
		Type: session.TurnEndEventName, Seq: nextSeq, Time: last.Time, Data: rawValue,
	})
	return closers, nil
}

func assistantToolCalls(entry session.Event) ([]llm.CallID, int64, error) {
	var payload struct {
		Turn    int64 `json:"turn"`
		Step    int64 `json:"step"`
		Message struct {
			Content []struct {
				Type string     `json:"type"`
				ID   llm.CallID `json:"id"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(entry.Data, &payload); err != nil || payload.Turn < 1 || payload.Step < 0 {
		return nil, 0, fmt.Errorf("invalid assistant/message at seq %d", entry.Seq)
	}
	result := make([]llm.CallID, 0)
	for _, block := range payload.Message.Content {
		if block.Type != "tool-call" {
			continue
		}
		if block.ID == "" {
			return nil, 0, fmt.Errorf("invalid assistant tool call at seq %d", entry.Seq)
		}
		result = append(result, block.ID)
	}
	return result, payload.Step, nil
}

func toolResultCallID(entry session.Event) (llm.CallID, error) {
	var payload struct {
		Message struct {
			Source struct {
				Kind   string     `json:"kind"`
				CallID llm.CallID `json:"callId"`
			} `json:"source"`
		} `json:"message"`
	}
	if err := json.Unmarshal(entry.Data, &payload); err != nil ||
		payload.Message.Source.Kind != "tool" || payload.Message.Source.CallID == "" {
		return "", fmt.Errorf("invalid tool/result at seq %d", entry.Seq)
	}
	return payload.Message.Source.CallID, nil
}

func decodeStepPosition(entry session.Event) (session.StepPosition, error) {
	var position session.StepPosition
	if err := decodeExact(entry.Data, &position); err != nil || position.Turn < 1 || position.Step < 0 {
		return session.StepPosition{}, fmt.Errorf("invalid %s at seq %d", entry.Type, entry.Seq)
	}
	return position, nil
}

func validateTurnEnd(entry session.Event) error {
	var payload struct {
		Turn   int64 `json:"turn"`
		Reason struct {
			Kind string `json:"kind"`
		} `json:"reason"`
	}
	if err := json.Unmarshal(entry.Data, &payload); err != nil || payload.Turn < 1 {
		return fmt.Errorf("invalid turn/end at seq %d", entry.Seq)
	}
	switch payload.Reason.Kind {
	case "completed", "blocked", "max-tokens", "interrupted", "aborted", "error":
		return nil
	default:
		return fmt.Errorf("invalid turn/end reason at seq %d", entry.Seq)
	}
}

func decodeExact[D any](rawValue json.RawMessage, target *D) error {
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func snapshotEvents(entries []session.Event) []session.Event {
	result := make([]session.Event, len(entries))
	for index, entry := range entries {
		result[index] = entry
		result[index].Data = append(json.RawMessage(nil), entry.Data...)
		if entry.SourceEventSeqs != nil {
			sequences := append([]int64(nil), (*entry.SourceEventSeqs)...)
			result[index].SourceEventSeqs = &sequences
		}
		if entry.SurfaceOp != nil {
			operation := *entry.SurfaceOp
			result[index].SurfaceOp = &operation
		}
	}
	return result
}

func eventPrefixesEqual(left []session.Event, right []session.Event) bool {
	return len(left) >= len(right) && reflect.DeepEqual(left[:len(right)], right)
}

func cloneHeader(metadata session.Header) session.Header {
	metadata.CWD = cloneTextPointer(metadata.CWD)
	metadata.ParentSession = cloneSessionPointer(metadata.ParentSession)
	metadata.SeedLength = cloneInt64Pointer(metadata.SeedLength)
	metadata.DelegationDepth = cloneInt64Pointer(metadata.DelegationDepth)
	metadata.AgentPreset = cloneTextPointer(metadata.AgentPreset)
	return metadata
}

func int64Pointer(value int64) *int64 {
	copyValue := value
	return &copyValue
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	return int64Pointer(*value)
}

func cloneTextPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneSessionPointer(value *session.SessionID) *session.SessionID {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}
