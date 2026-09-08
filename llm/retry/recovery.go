package llmretry

import (
	"errors"
	"math"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

type phase uint8

const (
	unstarted phase = iota
	delegating
	reading
	recordingSchedule
	waiting
	recordingStart
	completed
)

// Recovery owns one request-error decision, with no storage or scheduling dependencies.
type Recovery struct {
	phase        phase
	input        agent.RequestFailure
	sample       float64
	candidate    RetryID
	pending      *retryWrite
	started      *retryWrite
	delay        float64
	cancellation error
}

// NewRecovery captures the failed request and externally supplied entropy values.
func NewRecovery(input agent.RequestFailure, sample float64, candidate RetryID) (*Recovery, error) {
	if input.AgentID == "" || input.Provider == "" || input.Turn <= 0 || input.Step <= 0 {
		return nil, errors.New("llm-retry: request identity and position are required")
	}
	input.Failure = cloneFailure(input.Failure)
	if input.RetryPolicy != nil {
		input.RetryPolicy = input.RetryPolicy.CloneRetryPolicy()
	}
	return &Recovery{
		input:     input,
		sample:    sample,
		candidate: candidate,
	}, nil
}

// Operation is a recovery effect to be scheduled by the actor.
type Operation interface{ recoveryOperation() }

// ReadHistory requests committed retry facts.
type ReadHistory struct{}

// Continue requests the selected downstream recovery chain.
type Continue struct{}

// Commit requests an atomic, idempotent retry fact append.
type Commit struct{ Plan session.WritePlan }

// Wait delays the next generation until the committed retry schedule is due.
type Wait struct{ Milliseconds float64 }

// Completed contains the decision, independently of model generation.
type Completed struct {
	Action agent.RequestErrorAction
	Err    error
}

func (ReadHistory) recoveryOperation() {}
func (Continue) recoveryOperation()    {}
func (Commit) recoveryOperation()      {}
func (Wait) recoveryOperation()        {}
func (Completed) recoveryOperation()   {}

// Result is one completed effect, not a callback into the business owner.
type Result interface{ recoveryResult() }
type HistoryRead struct{ Snapshot session.Snapshot }
type Continued struct {
	Action agent.RequestErrorAction
	Err    error
}
type Committed struct{ Write session.WriteResult }
type Waited struct{}
type Failed struct{ Err error }

func (HistoryRead) recoveryResult() {}
func (Continued) recoveryResult()   {}
func (Committed) recoveryResult()   {}
func (Waited) recoveryResult()      {}
func (Failed) recoveryResult()      {}

// Start selects delegation or history inspection without performing either effect.
func (current *Recovery) Start() (Operation, error) {
	if current.phase != unstarted {
		return nil, errors.New("llm-retry: recovery already started")
	}
	switch policy := current.input.RetryPolicy.(type) {
	case nil, llm.AlwaysRetryPolicy:
		return current.delegate(), nil
	case llm.NormalRetryPolicy:
		if !retryable(policy, current.input.Failure.Code) {
			return current.delegate(), nil
		}
		current.phase = reading
		return ReadHistory{}, nil
	default:
		return current.finish(false, errors.New("llm-retry: unsupported retry policy")), nil
	}
}

// Cancel records intent without claiming the pending external operation has exited.
func (current *Recovery) Cancel(cause error) {
	if current.cancellation == nil && current.phase != completed {
		if cause == nil {
			cause = errors.New("llm-retry: recovery canceled")
		}
		current.cancellation = cause
	}
}

// Advance validates the result before choosing the next business effect.
func (current *Recovery) Advance(result Result) (Operation, error) {
	if current.phase == unstarted || current.phase == completed {
		return nil, errors.New("llm-retry: no effect is pending")
	}
	if failed, ok := result.(Failed); ok {
		if failed.Err == nil {
			return nil, errors.New("llm-retry: failed effect requires an error")
		}
		return current.finish(false, failed.Err), nil
	}
	switch current.phase {
	case delegating:
		response, ok := result.(Continued)
		if !ok {
			return nil, errors.New("llm-retry: expected downstream recovery result")
		}
		if current.cancellation != nil {
			return current.finish(false, nil), nil
		}
		if _, always := current.input.RetryPolicy.(llm.AlwaysRetryPolicy); !always || (response.Err == nil && response.Action.Retry) {
			return current.finish(response.Action.Retry, response.Err), nil
		}
		current.phase = reading
		return ReadHistory{}, nil
	case reading:
		response, ok := result.(HistoryRead)
		if !ok {
			return nil, errors.New("llm-retry: expected history")
		}
		if current.cancellation != nil {
			return current.finish(false, nil), nil
		}
		next, err := current.schedule(response.Snapshot)
		if err != nil {
			return current.finish(false, err), nil
		}
		return next, nil
	case recordingSchedule, recordingStart:
		response, ok := result.(Committed)
		if !ok {
			return nil, errors.New("llm-retry: expected commit acknowledgement")
		}
		write := response.Write
		if write.Barrier.SessionID != current.input.AgentID || len(write.Events) != 1 || write.NextSeq != write.FirstSeq+1 || write.Barrier.NextSeq != write.NextSeq || write.Events[0].Seq != write.FirstSeq {
			return current.finish(false, errors.New("llm-retry: invalid commit acknowledgement")), nil
		}
		if err := current.pending.draft.CheckCommit(write.Events[0]); err != nil {
			return current.finish(false, err), nil
		}
		if current.cancellation != nil {
			return current.finish(false, nil), nil
		}
		if current.phase == recordingStart {
			return current.finish(true, nil), nil
		}
		current.phase = waiting
		return Wait{
			Milliseconds: current.delay,
		}, nil
	case waiting:
		if _, ok := result.(Waited); !ok {
			return nil, errors.New("llm-retry: expected elapsed wait")
		}
		if current.cancellation != nil {
			return current.finish(false, nil), nil
		}
		current.pending = current.started
		current.phase = recordingStart
		return Commit{
			Plan: current.pending,
		}, nil
	}
	return nil, errors.New("llm-retry: unsupported recovery phase")
}

func (current *Recovery) delegate() Continue { current.phase = delegating; return Continue{} }
func (current *Recovery) finish(retry bool, failure error) Completed {
	current.phase = completed
	failure = errors.Join(current.cancellation, failure)
	return Completed{
		Action: agent.RequestErrorAction{
			Retry: retry && failure == nil,
		},
		Err: failure,
	}
}

func (current *Recovery) schedule(snapshot session.Snapshot) (Operation, error) {
	if err := checkSnapshot(snapshot, current.input.AgentID); err != nil {
		return nil, err
	}
	history, err := analyzeHistory(snapshot.Events)
	if err != nil {
		return nil, err
	}
	policy := current.input.RetryPolicy
	identity, err := policyKey(policy)
	if err != nil {
		return nil, err
	}
	prior, found := history.priorRetry(retryChainKey{
		turn:      current.input.Turn,
		step:      current.input.Step,
		provider:  current.input.Provider,
		policyKey: identity,
	})
	number := int64(1)
	chain := current.candidate
	if found {
		facts, err := factsFromRecord(prior)
		if err != nil {
			return nil, err
		}
		number = facts.retry + 1
		chain = facts.chainID
	}
	if bounded, normal := policy.(llm.NormalRetryPolicy); normal && number > bounded.MaxRetries {
		return current.delegate(), nil
	}
	if chain == "" {
		return nil, errors.New("llm-retry: new chain identity is required")
	}
	delay, delegate, err := selectDelay(current.input.Failure, policy, number, current.sample)
	if err != nil {
		return nil, err
	}
	if delegate {
		return current.delegate(), nil
	}
	var record RetryRecord
	switch configured := policy.(type) {
	case llm.NormalRetryPolicy:
		record = NormalRetryRecord{
			RetryID:    chain,
			Turn:       current.input.Turn,
			Step:       current.input.Step,
			Provider:   current.input.Provider,
			Mode:       llm.RetryNormal,
			PolicyKey:  identity,
			Retry:      number,
			MaxRetries: configured.MaxRetries,
			DelayMS:    delay,
			Failure:    current.input.Failure,
		}
	case llm.AlwaysRetryPolicy:
		record = AlwaysRetryRecord{
			RetryID:   chain,
			Turn:      current.input.Turn,
			Step:      current.input.Step,
			Provider:  current.input.Provider,
			Mode:      llm.RetryAlways,
			PolicyKey: identity,
			Retry:     number,
			DelayMS:   delay,
			Failure:   current.input.Failure,
		}
	}
	if err := history.addRetry(record); err != nil {
		return nil, err
	}
	draft, err := session.NewEventDraft(retryScheduledEvent, record)
	if err != nil {
		return nil, err
	}
	transition := RetryStarted{
		RetryID: chain,
		Turn:    current.input.Turn,
		Step:    current.input.Step,
		Retry:   number,
	}
	start, err := session.NewEventDraft(retryStartedEvent, transition)
	if err != nil {
		return nil, err
	}
	current.pending = &retryWrite{
		sessionID: current.input.AgentID,
		chain:     chain,
		number:    number,
		draft:     draft,
		scheduled: record,
	}
	current.started = &retryWrite{
		sessionID: current.input.AgentID,
		chain:     chain,
		number:    number,
		draft:     start,
		started:   &transition,
	}
	current.delay = delay
	current.phase = recordingSchedule
	return Commit{Plan: current.pending}, nil
}

func selectDelay(failure llm.LlmFailure, policy llm.RetryPolicy, number int64, sample float64) (float64, bool, error) {
	backoff := policy.RetryBackoff()
	if delay := failure.ProviderRetryAfterMS; delay != nil && !math.IsNaN(*delay) && !math.IsInf(*delay, 0) && *delay > 0 {
		if *delay <= backoff.MaxDelayMS {
			return *delay, false, nil
		}
		if _, normal := policy.(llm.NormalRetryPolicy); normal {
			return 0, true, nil
		}
	}
	delay, err := localDelay(policy, number, sample)
	return delay, false, err
}
