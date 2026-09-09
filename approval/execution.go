package approval

import (
	"context"
	"errors"

	"github.com/gorenx/goren/session"
)

// Journal commits approval-owned audit plans to an already selected Session.
// Implementations must return the actual original receipt when a plan is retried.
type Journal interface {
	Commit(context.Context, session.WritePlan) (session.WriteResult, error)
}

// Execution owns one approval's effects and retry boundary. Its caller serializes
// Run and retains it on error until the pending audit has been confirmed.
type Execution struct {
	decision *Decision
	phase    executionPhase
	pending  audit
	outcome  Outcome
}

type executionPhase uint8

const (
	// askedPending prevents contacting the answerer before the asked audit is confirmed.
	askedPending executionPhase = iota
	// answerPending owns the single interactive request after asked is committed.
	answerPending
	// decidedPending retains the fixed choice while its terminal audit is unconfirmed.
	decidedPending
	// executionResolved has confirmed both audit events and may return the outcome.
	executionResolved
)

// Begin fixes the policy, Turn and audit identity before executing any effects.
func (owner *Service) Begin(input Request) (*Execution, error) {
	prepared, err := owner.Prepare(input)
	if err != nil {
		return nil, err
	}
	draft, err := prepared.Asked()
	if err != nil {
		return nil, err
	}
	return &Execution{
		decision: prepared,
		pending: audit{
			decision:  prepared,
			eventType: AskedEventName,
			draft:     draft,
		},
	}, nil
}

// Run resumes only the unconfirmed step. User cancellation stops interaction but
// does not cancel the audit pair. The Journal must wait for the actual commitment result.
func (work *Execution) Run(ctx context.Context, records Journal, answerSource Answerer) (Outcome, error) {
	if ctx == nil || records == nil || work == nil || work.decision == nil {
		return "", errors.New("approval: execution, context and journal are required")
	}
	auditContext := context.WithoutCancel(ctx)
	if work.phase == askedPending {
		err := work.commit(auditContext, records)
		if err != nil {
			return "", err
		}
		work.phase = answerPending
	}
	if work.phase == answerPending {
		var choice Outcome
		var failure error
		if ctx.Err() == nil && work.decision.Interactive() && answerSource != nil {
			choice, failure = collectAnswer(ctx, answerSource, work.decision.Question())
		}
		resolved, err := work.decision.Resolve(choice, ctx.Err() != nil, failure)
		if err != nil {
			return "", err
		}
		work.pending = audit{
			decision:  work.decision,
			eventType: DecidedEventName,
			draft:     resolved.Draft,
		}
		work.outcome = resolved.Outcome
		work.phase = decidedPending
	}
	if work.phase == decidedPending {
		err := work.commit(auditContext, records)
		if err != nil {
			return "", err
		}
		work.phase = executionResolved
	}
	return work.outcome, nil
}

func (work *Execution) commit(ctx context.Context, records Journal) error {
	receipt, err := records.Commit(ctx, work.pending)
	if err != nil {
		return err
	}
	return work.pending.validateReceipt(receipt)
}

func collectAnswer(ctx context.Context, answerSource Answerer, input Question) (choice Outcome, failure error) {
	defer func() {
		if recover() != nil {
			choice = ""
			failure = errors.New("approval: answerer panicked")
		}
	}()
	return answerSource.Answer(ctx, input)
}
