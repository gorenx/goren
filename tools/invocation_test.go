package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/tools"
)

func TestInvocationReturnsDetachedEffectsAndRejectsLateCallbacks(t *testing.T) {
	message, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Source: agentmessage.UserMessageSource{},
		Content: []agentmessage.ContentBlock{
			agentmessage.TextBlock{
				Text: "followup",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var captured tools.ToolRunContext
	value := json.RawMessage(`{"ok":true}`)
	failure := errors.New("body failed")
	call := tools.Invocation{
		CallID:    "call",
		Name:      "tool",
		SessionID: "conversation",
		Arguments: json.RawMessage(`{"input":1}`),
	}
	result := call.Run(context.Background(), executorStub(func(arguments json.RawMessage, runContext tools.ToolRunContext) (json.RawMessage, error) {
		captured = runContext
		if runContext.Execution.CallID != call.CallID || runContext.Execution.SessionID != call.SessionID {
			t.Fatal("body identity changed")
		}
		arguments[0] = '!'
		runContext.Execution.Arguments[0] = '!'
		runContext.DeferContext(message)
		runContext.ConcludeTurn()
		return value, failure
	}))
	value[0] = '!'
	if string(call.Arguments) != `{"input":1}` || string(result.Value) != `{"ok":true}` {
		t.Fatal("invocation aliases arguments or body output")
	}
	if !errors.Is(result.Err, failure) || !result.ConcludesTurn || len(result.AdditionalContexts) != 1 {
		t.Fatalf("body result: %#v", result)
	}
	captured.DeferContext(message)
	captured.ConcludeTurn()
	if len(result.AdditionalContexts) != 1 {
		t.Fatal("late callback changed completed effects")
	}
	// Another body starts with its own effect collector, even for the same input.
	next := call.Run(context.Background(), executorStub(func(json.RawMessage, tools.ToolRunContext) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}))
	if next.ConcludesTurn || len(next.AdditionalContexts) != 0 || next.Err != nil {
		t.Fatalf("effects crossed invocations: %#v", next)
	}
}
