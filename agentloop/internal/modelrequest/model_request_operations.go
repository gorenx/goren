package modelrequest

import (
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

// RequestInput captures one Step's identity and prompt without model settings.
type RequestInput struct {
	SessionID session.SessionID
	Position  session.StepPosition
	Prompt    systemprompt.PromptAssembly
	First     bool
}

// RequestOperation describes one effect of the complete model request.
type RequestOperation interface {
	requestOperation()
}

// RunAttempt forwards one operation of the current model attempt.
type RunAttempt struct {
	Operation Operation
}

// ResolveRecovery invokes recovery only for a failed provider finish.
type ResolveRecovery struct {
	Input agent.RequestFailure
}

// RequestFinished retains business outcome and resource-release confirmation.
type RequestFinished struct {
	Result Completed
}

func (RunAttempt) requestOperation()      {}
func (ResolveRecovery) requestOperation() {}
func (RequestFinished) requestOperation() {}

// RequestResult carries one operation's detached completion or typed error.
type RequestResult interface{ requestResult() }

type AttemptAdvanced struct{ Result Result }
type RecoveryResolved struct{ Action agent.RequestErrorAction }
type RequestFailed struct{ Err error }

func (AttemptAdvanced) requestResult()  {}
func (RecoveryResolved) requestResult() {}
func (RequestFailed) requestResult()    {}
