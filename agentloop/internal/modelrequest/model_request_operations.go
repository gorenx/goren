package modelrequest

import (
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

// RequestInput captures one Step's prompt and configured proposal baseline.
type RequestInput struct {
	SessionID session.SessionID
	Position  session.StepPosition
	Prompt    systemprompt.PromptAssembly
	Config    llm.CallConfig
	First     bool
}

// RequestOperation describes one effect of the complete model request.
type RequestOperation interface{ requestOperation() }

// ReadRequest fetches a fresh Session snapshot, including after recovery.
type ReadRequest struct{}

// ResolveConfiguration invokes the request middleware before route validation.
type ResolveConfiguration struct {
	Input   agent.RequestInput
	Default llm.CallConfig
}

// PrepareCall requests effective parameters without opening a stream.
type PrepareCall struct {
	Config llm.CallConfig
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

func (ReadRequest) requestOperation()          {}
func (ResolveConfiguration) requestOperation() {}
func (PrepareCall) requestOperation()          {}
func (RunAttempt) requestOperation()           {}
func (ResolveRecovery) requestOperation()      {}
func (RequestFinished) requestOperation()      {}

// RequestResult carries one operation's detached completion or typed error.
type RequestResult interface{ requestResult() }

type RequestRead struct{ Snapshot session.Snapshot }
type ConfigurationResolved struct{ Config llm.CallConfig }

// CallPrepared contains metadata only; the adapter retains its actual binding.
type CallPrepared struct {
	Config      llm.CallConfig
	Defaults    llm.CallConfigAdapterDefaults
	Context     *llm.ModelContext
	RetryPolicy llm.RetryPolicy
}

type AttemptAdvanced struct{ Result Result }
type RecoveryResolved struct{ Action agent.RequestErrorAction }
type RequestFailed struct{ Err error }

func (RequestRead) requestResult()           {}
func (ConfigurationResolved) requestResult() {}
func (CallPrepared) requestResult()          {}
func (AttemptAdvanced) requestResult()       {}
func (RecoveryResolved) requestResult()      {}
func (RequestFailed) requestResult()         {}
