package agent

import "github.com/gorenx/goren/llm"

// Hooks is the narrow asynchronous dispatch port consumed by AgentLoop.
// Nil means an invocation was accepted, not that its middleware succeeded.
// Implementations deliver the named completion through their bound reply route.
// A returned error means dispatch did not start and no completion will follow.
// Correlation, cancellation transport and middleware registration stay outside
// this contract. Implementations must never block an actor mailbox for a result.
type Hooks interface {
	PreStep(input PreStepInput, fallback PreStepDecision) error
	Request(input RequestInput, fallback llm.CallConfig) error
	RequestError(input RequestFailure) error
	TurnStopping(input TurnStoppingInput) error
}

// PreStepResolved completes admission middleware, including its business error.
type PreStepResolved struct {
	Decision PreStepDecision
	Err      error
}

// RequestResolved completes request configuration before route validation.
type RequestResolved struct {
	Config llm.CallConfig
	Err    error
}

// RequestErrorResolved completes recovery; Retry is false by default.
type RequestErrorResolved struct {
	Action RequestErrorAction
	Err    error
}

// TurnStoppingCompleted reports hook completion, not Turn closure or Session flush.
type TurnStoppingCompleted struct {
	Err error
}

// HookResult is the closed set of middleware completion values. Technical
// correlation belongs to the transport envelope rather than these values.
type HookResult interface {
	hookResult()
}

func (PreStepResolved) hookResult()       {}
func (RequestResolved) hookResult()       {}
func (RequestErrorResolved) hookResult()  {}
func (TurnStoppingCompleted) hookResult() {}
