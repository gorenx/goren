package modelrequest

import (
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

// Start captures one Step and requests a fresh configuration baseline.
func (current *ModelRequest) Start(input RequestInput) (RequestOperation, error) {
	if current == nil || current.phase != requestUnstarted {
		return nil, errors.New("agentloop: model request already started or is nil")
	}
	if input.SessionID == "" || input.Position.Turn <= 0 || input.Position.Step <= 0 ||
		input.Position.Turn > 1<<53-1 || input.Position.Step > 1<<53-1 {
		return nil, errors.New("agentloop: invalid model request identity or position")
	}
	captured, err := input.Prompt.Clone()
	if err != nil {
		return nil, err
	}
	input.Prompt = captured
	input.Config = llm.CloneCallConfig(input.Config)
	current.input = input
	current.outcome = Completed{
		StreamClosed: true,
	}
	current.phase = readingHistory
	return ReadRequest{}, nil
}

// Advance consumes a matching result. Protocol errors leave the pending
// operation intact; operation failures become RequestFinished after cleanup.
func (current *ModelRequest) Advance(result RequestResult, cancellation error) (RequestOperation, error) {
	if err := current.checkCompletion(result); err != nil {
		return nil, err
	}
	if current.cancellation == nil {
		current.cancellation = cancellation
	}
	if current.phase == runningAttempt || current.phase == retryingClose {
		var response Result
		if failed, ok := result.(RequestFailed); ok {
			response = Failed{
				Err: failed.Err,
			}
		} else {
			response = result.(AttemptAdvanced).Result
		}
		if started, ok := response.(GenerationStarted); ok {
			current.proposal = llm.CloneCallConfig(started.Config)
			if started.RetryPolicy != nil {
				current.policy = started.RetryPolicy.CloneRetryPolicy()
			}
		}
		next, err := current.active.Advance(response, current.cancellation)
		if err != nil {
			return nil, err
		}
		return current.acceptAttempt(next), nil
	}
	if current.cancellation != nil {
		return current.finish(current.cancellation), nil
	}
	if failed, ok := result.(RequestFailed); ok {
		return current.finish(errors.Join(current.failure, failed.Err)), nil
	}
	switch current.phase {
	case readingHistory:
		return current.resolveConfiguration(result.(RequestRead).Snapshot), nil
	case resolvingConfig:
		resolved := result.(ConfigurationResolved)
		if resolved.Config.Provider == "" || resolved.Config.Model == "" {
			return current.finish(errors.New("agentloop: request middleware must resolve provider and model")), nil
		}
		current.proposal = llm.CloneCallConfig(resolved.Config)
		current.phase = savingConfiguration
		return SaveConfiguration{
			Config: llm.CloneCallConfig(current.proposal),
		}, nil
	case savingConfiguration:
		return current.startAttempt()
	case resolvingRecovery:
		if !result.(RecoveryResolved).Action.Retry {
			return current.finish(current.failure), nil
		}
		current.phase = readingHistory
		current.failure = nil
		return ReadRequest{}, nil
	}
	return nil, errors.New("agentloop: unsupported model request state")
}

func (current *ModelRequest) resolveConfiguration(snapshot session.Snapshot) RequestOperation {
	if snapshot.Barrier.SessionID != current.input.SessionID || snapshot.Barrier.NextSeq != int64(len(snapshot.Events)) {
		return current.finish(errors.New("agentloop: model request requires a complete Session snapshot"))
	}
	for index, entry := range snapshot.Events {
		if entry.Seq != int64(index) {
			return current.finish(errors.New("agentloop: model request Session sequence is discontinuous"))
		}
	}
	proposed := llm.CloneCallConfig(current.input.Config)
	if !current.input.First || current.headerCommitted {
		previous, err := session.LatestRequestHeader(snapshot.Events)
		if err != nil {
			return current.finish(err)
		}
		if previous == nil {
			return current.finish(errors.New("agentloop: confirmed request header is missing"))
		}
		proposed = Proposal(*previous)
	}
	current.phase = resolvingConfig
	return ResolveConfiguration{
		Input: agent.RequestInput{
			AgentID: current.input.SessionID,
			Turn:    current.input.Position.Turn,
			Step:    current.input.Position.Step,
		},
		Default: proposed,
	}
}

func (current *ModelRequest) startAttempt() (RequestOperation, error) {
	attempt, err := NewAttempt(AttemptInput{
		SessionID: current.input.SessionID,
		Position:  current.input.Position,
		Prompt:    current.input.Prompt,
		Config:    current.proposal,
		First:     current.input.First && !current.headerCommitted,
	})
	if err != nil {
		return current.finish(err), nil
	}
	current.active = attempt
	current.policy = nil
	current.outcome = Completed{}
	next, err := attempt.Start()
	if err != nil {
		return nil, err
	}
	current.attempts++
	current.phase = runningAttempt
	return RunAttempt{
		Operation: next,
	}, nil
}

func (current *ModelRequest) acceptAttempt(next Operation) RequestOperation {
	result, done := next.(Completed)
	if !done {
		return RunAttempt{
			Operation: next,
		}
	}
	current.headerCommitted = current.headerCommitted || result.HeaderCommitted
	current.outcome = result
	if current.phase == retryingClose {
		return current.finish(errors.Join(current.failure, result.Err))
	}
	if result.Err != nil || !result.StreamClosed || current.cancellation != nil {
		failure := result.Err
		if failure == nil && !result.StreamClosed {
			failure = errors.New("agentloop: model attempt has not released its stream")
		}
		return current.finish(failure)
	}
	if result.Finish == nil || (result.Finish.ReasonKind() != "error" && result.Finish.ReasonKind() != "aborted") {
		return current.finish(nil)
	}
	terminal, err := result.Finish.CloneReason()
	if err != nil {
		return current.finish(err)
	}
	var fact llm.LlmFailure
	switch reason := terminal.(type) {
	case llm.ErrorFinish:
		fact = reason.Failure
	case llm.AbortedFinish:
		fact = reason.Failure
	default:
		return current.finish(errors.New("agentloop: failed model finish is missing provider failure facts"))
	}
	problem, err := llm.NewLlmError(fact.Message, fact.Code, llm.LlmErrorOptions{
		Status:               fact.Status,
		ProviderRetryAfterMS: fact.ProviderRetryAfterMS,
		RequestID:            fact.RequestID,
	})
	if err != nil {
		return current.finish(err)
	}
	current.failure = problem
	var policy llm.RetryPolicy
	if current.policy != nil {
		policy = current.policy.CloneRetryPolicy()
	}
	current.phase = resolvingRecovery
	return ResolveRecovery{
		Input: agent.RequestFailure{
			AgentID:     current.input.SessionID,
			Turn:        current.input.Position.Turn,
			Step:        current.input.Position.Step,
			Provider:    current.proposal.Provider,
			Failure:     problem.Failure(),
			RetryPolicy: policy,
		},
	}
}

func (current *ModelRequest) finish(failure error) RequestFinished {
	current.failure = errors.Join(current.cancellation, failure)
	current.phase = requestComplete
	current.outcome.Err = current.failure
	current.outcome.HeaderCommitted = current.headerCommitted
	return RequestFinished{
		Result: current.outcome,
	}
}

// RetryClose retries only retained resource cleanup, never model generation.
func (current *ModelRequest) RetryClose() (RequestOperation, error) {
	if current == nil || current.phase != requestComplete {
		return nil, errors.New("agentloop: model request must finish before cleanup retry")
	}
	if current.outcome.StreamClosed {
		return RequestFinished{
			Result: current.outcome,
		}, nil
	}
	if current.active == nil {
		return nil, errors.New("agentloop: model request has no attempt to close")
	}
	next, err := current.active.RetryClose()
	if err != nil {
		return nil, err
	}
	current.phase = retryingClose
	return current.acceptAttempt(next), nil
}

func (current *ModelRequest) checkCompletion(result RequestResult) error {
	if current == nil || current.phase == requestUnstarted || current.phase == requestComplete {
		return errors.New("agentloop: model request has no pending operation")
	}
	if current.phase == runningAttempt || current.phase == retryingClose {
		if current.active == nil {
			return errors.New("agentloop: streaming request has no active attempt")
		}
		if advanced, ok := result.(AttemptAdvanced); ok {
			return current.active.checkResult(advanced.Result)
		}
		if failed, ok := result.(RequestFailed); ok {
			return current.active.checkResult(Failed{
				Err: failed.Err,
			})
		}
	}
	if failed, ok := result.(RequestFailed); ok {
		if failed.Err == nil {
			return errors.New("agentloop: failed request operation requires an error")
		}
		return nil
	}
	valid := false
	switch current.phase {
	case readingHistory:
		_, valid = result.(RequestRead)
	case resolvingConfig:
		_, valid = result.(ConfigurationResolved)
	case savingConfiguration:
		_, valid = result.(ConfigurationSaved)
	case runningAttempt, retryingClose:
		_, valid = result.(AttemptAdvanced)
	case resolvingRecovery:
		_, valid = result.(RecoveryResolved)
	}
	if !valid {
		return errors.New("agentloop: completion does not match the model request operation")
	}
	return nil
}
