package tools

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

// Invocation is detached tool-body input. It contains no scheduler, runtime or
// registration owner or same-process runtime tokens.
type Invocation struct {
	CallID     agentmessage.CallID
	RootCallID agentmessage.CallID
	Name       string
	SessionID  session.SessionID
	Arguments  json.RawMessage
}

// BodyResult returns body output and effects for the Runtime to normalize and
// admit. It is not a completed pipeline result or a Session commit receipt.
type BodyResult struct {
	Value              json.RawMessage
	Err                error
	AdditionalContexts []agentmessage.UserMessage
	ConcludesTurn      bool
}

// Invocation captures the body input without exposing internal execution state.
func (toolCall ToolExecution) Invocation() Invocation {
	return Invocation{
		CallID:     toolCall.CallID,
		RootCallID: toolCall.RootCallID,
		Name:       toolCall.Name,
		SessionID:  toolCall.SessionID,
		Arguments:  append(json.RawMessage(nil), toolCall.arguments...),
	}
}

// ArgumentsJSON returns an independent copy for tool implementations.
func (call Invocation) ArgumentsJSON() json.RawMessage {
	return append(json.RawMessage(nil), call.Arguments...)
}

// Run executes one body and seals its effects before returning. Policy,
// parameter validation and result normalization remain with Tool Runtime.
func (call Invocation) Run(ctx context.Context, handler Executor) BodyResult {
	effects := &bodyEffects{}
	call.Arguments = call.ArgumentsJSON()
	value, err := invokeExecutor(handler, call.ArgumentsJSON(), ToolRunContext{
		Context:   ctx,
		Execution: call,
		effects:   effects,
	})
	effects.mu.Lock()
	defer effects.mu.Unlock()
	effects.closed = true
	contexts, _ := cloneUserMessages(effects.contexts)
	return BodyResult{
		Value:              append(json.RawMessage(nil), value...),
		Err:                err,
		AdditionalContexts: contexts,
		ConcludesTurn:      effects.concludesTurn,
	}
}

// bodyEffects synchronizes a body's callbacks with its return boundary. It has
// no link to the Runtime; closed bodies cannot mutate a previously returned result.
type bodyEffects struct {
	mu            sync.Mutex
	closed        bool
	contexts      []agentmessage.UserMessage
	concludesTurn bool
}
