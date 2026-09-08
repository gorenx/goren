// Package middlewareactor adapts named Agent middleware calls to GoAkt requests.
package middlewareactor

import (
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/middleware"
	"github.com/gorenx/goren/llm"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// CallID correlates one process-local invocation, never a persisted business fact.
type CallID string

// Route binds a selected middleware definition to its module Grain.
type Route struct {
	Definition middleware.RegistrationID
	Target     *goakt.GrainIdentity
}

// Invocation identifies a single selected handler and the route for Continue.
type Invocation struct {
	CallID      CallID
	Handler     middleware.RegistrationID
	Coordinator *goakt.GrainIdentity
}

// CancelCall signals cancellation; success does not assert that handlers have exited.
type CancelCall struct {
	CallID CallID
	Cause  error
}

// CancelInvocation tells an active module to cancel and finish its original Invoke.
// It must not replace the completion reply to the original request.
type CancelInvocation struct {
	CallID  CallID
	Handler middleware.RegistrationID
	Cause   error
}

// Completion acknowledges a technical command, retaining its original error.
type Completion struct {
	Err error
}

// ResolvePreStep runs a fixed selection; an empty selection returns Default.
// Callers use a fresh CallID for every call.
type ResolvePreStep struct {
	CallID  CallID
	Routes  []Route
	Input   agent.PreStepInput
	Default agent.PreStepDecision
}

// InvokePreStep asks a module to run its business middleware.
type InvokePreStep struct {
	Invocation Invocation
	Input      agent.PreStepInput
}

// ContinuePreStep requests this handler's downstream result once.
type ContinuePreStep struct {
	CallID  CallID
	Handler middleware.RegistrationID
}

// ResolveRequest resolves configuration before model route validation.
// An empty selection returns Default; callers use a fresh CallID for every call.
type ResolveRequest struct {
	CallID  CallID
	Routes  []Route
	Input   agent.RequestInput
	Default llm.CallConfig
}

// InvokeRequest asks a module to run its business middleware.
type InvokeRequest struct {
	Invocation Invocation
	Input      agent.RequestInput
}

// ContinueRequest requests this handler's downstream result once.
type ContinueRequest struct {
	CallID  CallID
	Handler middleware.RegistrationID
}

// ResolveRequestError handles a provider failure, defaulting to no retry.
// Callers use a fresh CallID for every call.
type ResolveRequestError struct {
	CallID CallID
	Routes []Route
	Input  agent.RequestFailure
}

// InvokeRequestError asks a module to run its business middleware.
type InvokeRequestError struct {
	Invocation Invocation
	Input      agent.RequestFailure
}

// ContinueRequestError requests this handler's downstream result once.
type ContinueRequestError struct {
	CallID  CallID
	Handler middleware.RegistrationID
}

// RunTurnStopping awaits a fixed list of serial hooks; an empty list succeeds.
// The AgentLoop must check committed Inbox input again after completion.
type RunTurnStopping struct {
	CallID CallID
	Routes []Route
	Input  agent.TurnStoppingInput
}

// InvokeTurnStopping asks a hook to await its work, including Steer confirmation.
type InvokeTurnStopping struct {
	Invocation Invocation
	Input      agent.TurnStoppingInput
}
