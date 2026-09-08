package agent

import (
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

// PreStepKind selects rejection or entry for a proposed model step.
type PreStepKind string

const (
	// PreStepReject prevents the proposed model step.
	PreStepReject PreStepKind = "reject"
	// PreStepEnter admits the proposed model step.
	PreStepEnter PreStepKind = "enter"
)

// PreStepInput is the detached input of pre-step admission.
type PreStepInput struct {
	AgentID  session.SessionID
	Messages []agentmessage.UserMessage
	Turn     int64
	Step     int64
}

// PreStepDecision decides whether and with which messages a step starts.
type PreStepDecision struct {
	Kind     PreStepKind
	Messages []agentmessage.UserMessage
}

// RequestInput identifies the step whose immutable call config is resolving.
type RequestInput struct {
	AgentID session.SessionID
	Turn    int64
	Step    int64
}

// RequestResolution is the resolved model parameters for a request.
type RequestResolution struct {
	Config llm.CallConfig
}

// RequestFailure contains provider-neutral failed-attempt policy facts.
type RequestFailure struct {
	AgentID     session.SessionID
	Turn        int64
	Step        int64
	Provider    string
	Failure     llm.LlmFailure
	RetryPolicy llm.RetryPolicy
}

// RequestErrorAction states whether recovery permits another attempt.
type RequestErrorAction struct {
	Retry bool
}

// TurnStoppingInput identifies the turn whose ordered hooks must finish.
type TurnStoppingInput struct {
	AgentID session.SessionID
	Turn    int64
}
