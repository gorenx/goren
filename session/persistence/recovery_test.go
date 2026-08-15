package persistence

import (
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

func TestInterruptedTurnClosersPreserveUnknownToolOutcomeSemantics(t *testing.T) {
	t.Parallel()
	conversation, err := session.New("tool-recovery", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Append(conversation, session.TurnStarted, session.TurnStart{Turn: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Append(conversation, session.StepStarted, session.StepPosition{Turn: 1, Step: 1}); err != nil {
		t.Fatal(err)
	}
	assistantMessage, err := llm.NewAssistantMessage(llm.AssistantMessageInput{
		Content: []llm.ContentBlock{llm.ToolCallBlock{
			Type: "tool-call", ID: "call-1", Name: "probe", Arguments: `{}`,
		}},
		Source: llm.ModelMessageSource{Provider: "mock", Model: "mock-model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.AppendSurface(
		conversation, session.AssistantMessaged,
		session.AssistantMessage{Turn: 1, Step: 1, Message: assistantMessage},
		session.SurfaceIntent{Operation: session.SurfaceAppend()},
	); err != nil {
		t.Fatal(err)
	}
	toolCall, err := session.Append(conversation, session.ToolCalled, session.ToolCall{
		Turn: 1, Step: 1, CallID: "call-1", Name: "probe", Arguments: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	closers, err := interruptedTurnClosers(conversation.Events())
	if err != nil {
		t.Fatal(err)
	}
	if len(closers) != 3 || closers[0].Type != session.ToolResultEventName ||
		closers[1].Type != session.StepEndEventName || closers[2].Type != session.TurnEndEventName ||
		closers[0].SourceEventSeqs == nil || len(*closers[0].SourceEventSeqs) != 1 ||
		(*closers[0].SourceEventSeqs)[0] != toolCall.Seq {
		t.Fatalf("closers = %#v", closers)
	}
	var payload struct {
		Error session.ToolErrorInfo `json:"error"`
	}
	if err := json.Unmarshal(closers[0].Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Name != "ToolOutcomeUnknownError" || payload.Error.Code != toolOutcomeUnknownCode {
		t.Fatalf("tool recovery error = %#v", payload.Error)
	}
	combined := append(conversation.Events(), closers...)
	if _, err := inspectStored(conversation.Header(), combined); err != nil {
		t.Fatalf("recovered log does not reconstruct: %v", err)
	}
}
