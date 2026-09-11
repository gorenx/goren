package basic

import (
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

// Recovery owns one Agent's consecutive overflow recovery budget.
// Middleware invocation identities and delivery deduplication belong to the actor.
type Recovery struct {
	identifier session.SessionID
	settings   ResolvedConfig
	turn       int64
	response   int64
	retries    int
	recovering bool
}

// RecoveryDecision separates a permitted retry from a reportable compaction error.
// Diagnostic must not be returned as the middleware error when Retry is true.
type RecoveryDecision struct {
	Retry      bool
	Diagnostic error
}

// NewRecovery binds a budget to one Agent without retaining its runtime object.
func NewRecovery(identifier session.SessionID, settings ResolvedConfig) (*Recovery, error) {
	if identifier == "" {
		return nil, errors.New("compaction: recovery requires an Agent identity")
	}
	return &Recovery{
		identifier: identifier,
		settings:   cloneResolvedConfig(settings),
		response:   -1,
	}, nil
}

// Prepare approves at most one active overflow attempt using committed reset facts.
func (subject *Recovery) Prepare(input agent.RequestFailure, snapshot session.Snapshot) (bool, error) {
	if subject.recovering {
		return false, errors.New("compaction: overflow recovery is already active")
	}
	if input.AgentID != subject.identifier || snapshot.Barrier.SessionID != subject.identifier || input.Turn <= 0 || input.Step <= 0 || snapshot.Barrier.NextSeq != int64(len(snapshot.Events)) {
		return false, errors.New("compaction: invalid recovery identity or snapshot")
	}
	state, err := compaction.InspectLog(snapshot.Events)
	if err != nil {
		return false, err
	}
	if state.OpenTurn == nil || *state.OpenTurn != input.Turn {
		return false, errors.New("compaction: recovery does not belong to the current Turn")
	}
	latest := int64(-1)
	for index, entry := range snapshot.Events {
		if entry.Seq != int64(index) {
			return false, errors.New("compaction: recovery requires a contiguous Session log")
		}
		if entry.Type == session.AssistantMessageEventName {
			latest = entry.Seq
		}
	}
	if input.Turn < subject.turn || latest < subject.response {
		return false, errors.New("compaction: recovery snapshot precedes the current failure chain")
	}
	if input.Turn != subject.turn || latest != subject.response {
		subject.turn = input.Turn
		subject.response = latest
		subject.retries = 0
	}
	if !subject.settings.Auto || input.Failure.Code != llm.ContextWindowExceededCode {
		return false, nil
	}
	target, found, err := routedTarget(snapshot)
	if err != nil || !found {
		return false, err
	}
	policy := ResolveTargetPolicy(subject.settings, target)
	if subject.retries >= policy.MaxOverflowRetries {
		return false, nil
	}
	subject.recovering = true
	return true, nil
}

// Complete consumes only a settled Overflow invocation, preserving cancellation priority.
func (subject *Recovery) Complete(execution *Compaction, cancellation error) (RecoveryDecision, error) {
	if !subject.recovering {
		return RecoveryDecision{}, errors.New("compaction: overflow recovery is not active")
	}
	if execution == nil || execution.mode != Overflow ||
		execution.barrier.SessionID != subject.identifier ||
		execution.Stage() != CompactionFinished {
		return RecoveryDecision{}, errors.New("compaction: overflow invocation is unfinished or belongs to another Agent")
	}
	outcome, failure := execution.Result()
	subject.recovering = false
	if cancellation != nil {
		return RecoveryDecision{}, errors.Join(cancellation, failure)
	}
	if outcome.Changed {
		subject.retries++
	}
	return RecoveryDecision{
		Retry:      outcome.Changed,
		Diagnostic: failure,
	}, nil
}

// Reject cancels admission when no Compaction invocation was started.
// A transport timeout after dispatch is not proof of rejection.
func (subject *Recovery) Reject() error {
	if !subject.recovering {
		return errors.New("compaction: overflow recovery is not active")
	}
	subject.recovering = false
	return nil
}
