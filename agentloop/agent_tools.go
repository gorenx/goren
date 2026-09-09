package agentloop

import (
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/agent"
	stepstate "github.com/gorenx/goren/agentloop/internal/step"
	"github.com/gorenx/goren/agentloop/internal/toolbatch"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

// CompleteTools prepares Step settlement after the current local batch closes.
// The caller associates the batch with this execution. Ordinary tool failure
// results belong to the model conversation; failure is a batch execution error.
// Pending next-step input is preserved, including when a tool concludes the Step.
func (subject *ReactLoopAgent) CompleteTools(batch *toolbatch.ToolBatch, failure error) (EndStep, error) {
	if subject.step == nil || subject.step.StateValue() != stepstate.StateTooling {
		return EndStep{}, errors.New("agentloop: tool completion requires a current tooling Step")
	}
	if !batch.IsComplete() {
		return EndStep{}, errors.New("agentloop: tool batch must close before Step settlement")
	}
	var cancelled agent.Cancellation
	isCancellation := errors.As(failure, &cancelled)
	switch batch.StopReason() {
	case toolbatch.Canceled:
		if !isCancellation {
			return EndStep{}, errors.New("agentloop: canceled tool batch requires its cancellation error")
		}
	case toolbatch.ExecutionFailed:
		if failure == nil {
			return EndStep{}, errors.New("agentloop: failed tool batch requires its execution error")
		}
	}
	conclusion := stepstate.StepResultContinue
	switch {
	case isCancellation:
		conclusion = stepstate.StepResultAborted
	case failure != nil:
		conclusion = stepstate.StepResultError
	case batch.StopsModelContinuation():
		conclusion = stepstate.StepResultCompleted
	}
	return subject.endStep(conclusion, failure)
}

// toolCalls owns the business progression of a Step's calls. Transport receivers
// retain it only while their operations are outstanding; it retains no Client.
type toolCalls struct {
	work  *ReactLoopAgent
	batch *toolbatch.ToolBatch
	calls []agentmessage.ToolCallBlock
	// index is the zero-based position of the current call in calls and batch,
	// not a tool identity. For [read_file, edit_file], it remains 0 through
	// result/context persistence and resource release, then advances to 1.
	index    int
	result   tools.ToolExecutionResult
	contexts []agentmessage.UserMessage
}

func (work *ReactLoopAgent) executeTools(services Services, input ExecuteTools) {
	if services.Tools == nil {
		services.Observer.ExecutionFailed(errors.New("agentloop: tool capability is missing"))
		return
	}
	batch, err := toolbatch.New(len(input.Calls), 1)
	if err != nil {
		work.failStep(services, err)
		return
	}
	calls := &toolCalls{
		work:  work,
		batch: batch,
		calls: input.Calls,
	}
	calls.start(services)
}

func (calls *toolCalls) start(services Services) {
	if calls.index == len(calls.calls) {
		err := calls.batch.Complete()
		if err != nil {
			services.Observer.ExecutionFailed(err)
			return
		}
		ending, err := calls.work.CompleteTools(calls.batch, calls.work.Cancellation())
		if err != nil {
			services.Observer.ExecutionFailed(err)
			return
		}
		calls.work.commitStepEnd(services, ending)
		return
	}
	if calls.work.Cancellation() != nil {
		calls.cancelRemaining(services)
		return
	}
	err := calls.batch.StartCall(calls.index)
	if err != nil {
		services.Observer.ExecutionFailed(err)
		return
	}
	draft, err := calls.work.PrepareToolCall(calls.calls[calls.index])
	if err != nil {
		services.Observer.ExecutionFailed(err)
		return
	}
	writing := &toolCallCommit{
		calls: calls,
		fact:  calls.write(session.ToolCallEventName, draft),
	}
	writing.fact.commit(services, writing)
}

// cancelRemaining records only calls that never entered the Tool Runtime.
// An already started call reaches here only after its real result and release.
func (calls *toolCalls) cancelRemaining(services Services) {
	if calls.batch.StopReason() == toolbatch.NotStopped {
		err := calls.batch.StopDispatch(toolbatch.Canceled)
		if err != nil {
			services.Observer.ExecutionFailed(err)
			return
		}
	}
	call := calls.calls[calls.index]
	called, err := calls.work.PrepareToolCall(call)
	if err != nil {
		services.Observer.ExecutionFailed(err)
		return
	}
	outcome := &tools.ToolExecutionFailure{
		Error: tools.ToolFailure{
			Message: "tool call aborted before dispatch",
			Info: &tools.ToolErrorInfo{
				Name: "AbortError",
				Code: tools.ToolAbortedBeforeDispatch,
			},
		},
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("Error: tool call aborted before dispatch")},
	}
	result, err := calls.work.PrepareToolResult(call, outcome)
	if err != nil {
		services.Observer.ExecutionFailed(err)
		return
	}
	writing := &toolCancellation{
		calls: calls,
		fact:  calls.write(session.ToolCallEventName, called, result),
	}
	writing.fact.commit(services, writing)
}

// toolCancellation waits for the complete pair before advancing model order.
type toolCancellation struct {
	calls *toolCalls
	fact  toolWrite
}

func (writing *toolCancellation) SessionCommitted(services Services, result session.WriteResult, failure error) {
	if failure == nil {
		failure = writing.calls.work.checkCommit(writing.fact.drafts, result)
	}
	if failure == nil {
		failure = writing.calls.batch.AcceptCancellation(writing.calls.index)
	}
	if failure != nil {
		retry := *writing
		retry.calls = writing.calls.checkpoint()
		writing.calls.work.retry = retry
		services.Observer.ExecutionFailed(failure)
		return
	}
	writing.calls.index++
	writing.calls.start(services)
}

type toolCallCommit struct {
	calls *toolCalls
	fact  toolWrite
}

func (writing *toolCallCommit) SessionCommitted(services Services, result session.WriteResult, failure error) {
	if failure == nil {
		failure = writing.calls.work.checkCommit(writing.fact.drafts, result)
	}
	if failure != nil {
		retry := *writing
		retry.calls = writing.calls.checkpoint()
		writing.calls.work.retry = retry
		services.Observer.ExecutionFailed(failure)
		return
	}
	calls := writing.calls
	call := calls.calls[calls.index]
	err := services.Tools.Prepare(tools.ToolExecutionInput{
		CallID:    call.ID,
		Name:      call.Name,
		Arguments: json.RawMessage(call.Arguments),
		SessionID: calls.work.ID(),
	}, calls)
	if err != nil {
		calls.ToolPrepared(services, "", nil, err)
	}
}

func (calls *toolCalls) ToolPrepared(
	services Services,
	stage tools.ScheduledToolStage,
	request *tools.ApprovalRequest,
	failure error,
) {
	if failure != nil {
		services.Observer.ExecutionFailed(failure)
		return
	}
	identifier := calls.calls[calls.index].ID
	var err error
	switch stage {
	case tools.ScheduledDispatch:
		err = services.Tools.Dispatch(identifier, calls)
	case tools.ScheduledApproval:
		if request == nil || services.Approvals == nil {
			services.Observer.ExecutionFailed(errors.New("agentloop: tool approval requires its request and approval capability"))
			return
		}
		err = services.Approvals.Decide(*request, calls)
	case tools.ScheduledFinalResult, tools.ScheduledPostResult:
		err = services.Tools.Finalize(identifier, calls)
	default:
		err = errors.New("agentloop: invalid tool preparation stage")
	}
	if err != nil {
		services.Observer.ExecutionFailed(err)
	}
}

func (calls *toolCalls) ApprovalDecided(services Services, outcome approval.Outcome, failure error) {
	if failure != nil {
		services.Observer.ExecutionFailed(failure)
		return
	}
	err := services.Tools.ApplyApproval(calls.calls[calls.index].ID, outcome, calls)
	if err != nil {
		calls.ToolPrepared(services, "", nil, err)
	}
}

func (calls *toolCalls) ToolDispatched(services Services, failure error) {
	if failure != nil {
		services.Observer.ExecutionFailed(failure)
		return
	}
	err := services.Tools.Finalize(calls.calls[calls.index].ID, calls)
	if err != nil {
		calls.ToolFinalized(services, nil, err)
	}
}

func (calls *toolCalls) ToolFinalized(services Services, outcome tools.ToolExecutionResult, failure error) {
	if failure == nil && outcome == nil {
		failure = errors.New("agentloop: tool result is missing")
	}
	if failure != nil {
		services.Observer.ExecutionFailed(failure)
		return
	}
	err := calls.batch.CompleteCall(calls.index)
	if err != nil {
		services.Observer.ExecutionFailed(err)
		return
	}
	calls.result = outcome
	draft, err := calls.work.PrepareToolResult(calls.calls[calls.index], outcome)
	if err != nil {
		services.Observer.ExecutionFailed(err)
		return
	}
	writing := &toolResultCommit{
		calls: calls,
		fact:  calls.write(session.ToolResultEventName, draft),
	}
	writing.fact.commit(services, writing)
}

type toolResultCommit struct {
	calls *toolCalls
	fact  toolWrite
}

func (writing *toolResultCommit) SessionCommitted(services Services, result session.WriteResult, failure error) {
	if failure == nil {
		failure = writing.calls.work.checkCommit(writing.fact.drafts, result)
	}
	if failure != nil {
		retry := *writing
		retry.calls = writing.calls.checkpoint()
		writing.calls.work.retry = retry
		services.Observer.ExecutionFailed(failure)
		return
	}
	calls := writing.calls
	err := calls.batch.AcceptResult(calls.index, calls.result.ConcludesAgentTurn())
	if err != nil {
		services.Observer.ExecutionFailed(err)
		return
	}
	calls.contexts = calls.result.AdditionalContextMessages()
	calls.injectContext(services)
}

func (calls *toolCalls) injectContext(services Services) {
	if len(calls.contexts) == 0 {
		err := services.Tools.Release(calls.calls[calls.index].ID, calls)
		if err != nil {
			calls.ToolReleased(services, err)
		}
		return
	}
	proposal, err := calls.work.Inject(calls.contexts[0])
	if err != nil {
		services.Observer.ExecutionFailed(err)
		return
	}
	writing := &toolContextCommit{
		calls:    calls,
		proposal: proposal,
	}
	err = services.Conversation.Commit(session.Batch(proposal.Edit.Drafts...), writing)
	if err != nil {
		writing.SessionCommitted(services, session.WriteResult{}, err)
	}
}

type toolContextCommit struct {
	calls    *toolCalls
	proposal agent.Admission
}

func (writing *toolContextCommit) SessionCommitted(services Services, result session.WriteResult, failure error) {
	if failure == nil {
		_, failure = writing.calls.work.ConfirmAdmission(writing.proposal, result)
	}
	if failure != nil {
		services.Observer.ExecutionFailed(failure)
		return
	}
	writing.calls.contexts = writing.calls.contexts[1:]
	writing.calls.injectContext(services)
}

func (calls *toolCalls) ToolReleased(services Services, failure error) {
	if failure != nil {
		services.Observer.ExecutionFailed(failure)
		return
	}
	calls.index++
	calls.result = nil
	calls.start(services)
}

// PrepareToolCall records raw model arguments before host execution begins.
func (subject *ReactLoopAgent) PrepareToolCall(input agentmessage.ToolCallBlock) (session.EventDraft, error) {
	if subject.step == nil || subject.step.StateValue() != stepstate.StateTooling {
		return session.EventDraft{}, ErrState
	}
	turnNumber, stepNumber := subject.step.Position()
	return session.NewEventDraft(session.ToolCalled, session.ToolCall{
		Turn:      turnNumber,
		Step:      stepNumber,
		CallID:    input.ID,
		Name:      input.Name,
		Arguments: input.Arguments,
	})
}

// PrepareToolResult maps the final Tool outcome into the model-visible Surface.
// Additional contexts are admitted only after this result has committed.
func (subject *ReactLoopAgent) PrepareToolResult(input agentmessage.ToolCallBlock, outcome tools.ToolExecutionResult) (session.EventDraft, error) {
	if subject.step == nil || subject.step.StateValue() != stepstate.StateTooling || outcome == nil {
		return session.EventDraft{}, errors.New("agentloop: Tool result requires the current tooling Step")
	}
	message, err := agentmessage.NewToolResultMessage(agentmessage.ToolResultMessageInput{
		CallID:  input.ID,
		Content: outcome.ContentBlocks(),
		IsError: outcome.Failed(),
	})
	if err != nil {
		return session.EventDraft{}, err
	}
	turnNumber, stepNumber := subject.step.Position()
	result := session.ToolResult{
		Turn:    turnNumber,
		Step:    stepNumber,
		Message: message,
		Meta:    outcome.PresentationMeta(),
	}
	if failure, ok := outcome.FailureDetail(); ok && failure.Info != nil {
		result.Error = &session.ToolErrorInfo{
			Name: failure.Info.Name,
			Code: failure.Info.Code,
		}
	}
	return session.NewSurfaceEventDraft(
		session.ToolResultAdded,
		result,
		session.SurfaceIntent{Operation: session.SurfaceAppend()},
	)
}

func (writing toolCallCommit) resume(subject *ReactLoopAgent, services Services) {
	writing.calls.work = subject
	writing.fact.commit(services, &writing)
}

func (writing toolResultCommit) resume(subject *ReactLoopAgent, services Services) {
	writing.calls.work = subject
	writing.fact.commit(services, &writing)
}

func (writing toolCancellation) resume(subject *ReactLoopAgent, services Services) {
	writing.calls.work = subject
	writing.fact.commit(services, &writing)
}

func (calls *toolCalls) write(eventType string, drafts ...session.EventDraft) toolWrite {
	turnNumber, stepNumber := calls.work.step.Position()
	return toolWrite{
		sessionID: calls.work.ID(),
		position: session.StepPosition{
			Turn: turnNumber,
			Step: stepNumber,
		},
		callID:    calls.calls[calls.index].ID,
		eventType: eventType,
		drafts:    drafts,
	}
}

// checkpoint detaches the execution owner before a retry is retained by it.
// Run removes the retry first; resume then reattaches the owner to the receiver.
// The retained graph therefore never points back from a child to its owner.
func (calls *toolCalls) checkpoint() *toolCalls {
	detached := *calls
	detached.work = nil
	return &detached
}
