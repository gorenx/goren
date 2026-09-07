package approval

import (
	"errors"
	"github.com/gorenx/goren/session"
)

// Decision fixes the policy and audit identity of one approval, without tracking I/O.
type Decision struct {
	policy   Policy
	question Question
}

// Resolution contains a terminal outcome and the audit draft that must be committed first.
type Resolution struct {
	Outcome Outcome
	Draft   session.EventDraft
}

// Question returns a detached answerer request. The caller must first commit Asked.
func (owner *Decision) Question() Question {
	result := owner.question
	result.CallID = cloneCallID(result.CallID)
	result.Reason = cloneString(result.Reason)
	return result
}

// Interactive reports whether the fixed policy permits contacting an answerer.
func (owner *Decision) Interactive() bool { return owner.policy == PolicyAsk }

// Asked prepares the pre-dispatch audit; it does not record or dispatch anything.
func (owner *Decision) Asked() (session.EventDraft, error) {
	if err := owner.validate(); err != nil {
		return session.EventDraft{}, err
	}
	return session.NewEventDraft(AskedEvent, Asked{
		ID:       owner.question.ID,
		ToolName: owner.question.ToolName,
		CallID:   cloneCallID(owner.question.CallID),
		Reason:   cloneString(owner.question.Reason),
	})
}

// Resolve fails closed and gives cancellation precedence over a late answer.
// The caller must commit Draft before exposing Outcome to the tool execution.
func (owner *Decision) Resolve(answer Outcome, cancelled bool, failure error) (Resolution, error) {
	if err := owner.validate(); err != nil {
		return Resolution{}, err
	}
	selected := answer
	switch {
	case cancelled:
		selected = OutcomeCancelled
	case owner.policy == PolicyNever:
		selected = OutcomeRejected
	case failure != nil || !validOutcome(answer):
		selected = OutcomeUnavailable
	}
	draft, err := session.NewEventDraft(DecidedEvent, Decided{
		ID:      owner.question.ID,
		Outcome: selected,
	})
	if err != nil {
		return Resolution{}, err
	}
	return Resolution{
		Outcome: selected,
		Draft:   draft,
	}, nil
}

func (owner *Decision) validate() error {
	if owner == nil || !validPolicy(owner.policy) || owner.question.ID == "" || owner.question.SessionID == "" || owner.question.ToolName == "" {
		return errors.New("approval: decision was not prepared")
	}
	return nil
}
