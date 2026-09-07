package approval

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/gorenx/goren/session"
)

// Service owns approval policy and paired durable audit events.
// Answer waiting and resource cleanup belong to the supplied Answerer.
type Service struct {
	defaultPolicy Policy
	answerer      Answerer
}

// New constructs an independent approval service from validated configuration.
func New(settings ValidatedConfig, responder Answerer) (*Service, error) {
	if !validPolicy(settings.policy) {
		return nil, errors.New("approval: configuration was not validated")
	}
	return &Service{
		defaultPolicy: settings.policy,
		answerer:      responder,
	}, nil
}

// Request writes the paired audit events around one policy/answerer decision.
func (owner *Service) Request(
	ctx context.Context,
	decisionInput Request,
) (Outcome, error) {
	if ctx == nil || decisionInput.Subject == nil ||
		decisionInput.Subject.SessionValue() == nil {
		return "", errors.New("approval: Context, Subject, and Session are required")
	}
	conversation := decisionInput.Subject.SessionValue()
	if !hasOpenTurn(conversation.Events()) {
		return "", errors.New(
			"approval: request outside an open turn; audit events must be turn-enclosed",
		)
	}
	identifier, err := mintRequestID()
	if err != nil {
		return "", fmt.Errorf("approval: mint request id: %w", err)
	}
	if identifier == "" {
		return "", errors.New("approval: minted request id is empty")
	}
	askedDraft, err := session.NewEventDraft(
		AskedEvent,
		Asked{
			ID:       identifier,
			ToolName: decisionInput.ToolName,
			CallID:   cloneCallID(decisionInput.CallID),
			Reason:   cloneString(decisionInput.Reason),
		},
	)
	if err != nil {
		return "", err
	}
	if _, err = conversation.Commit(ctx, session.Batch(askedDraft)); err != nil {
		return "", err
	}
	decisionOutcome := owner.decide(
		ctx,
		Question{
			ID:        identifier,
			SessionID: conversation.ID(),
			ToolName:  decisionInput.ToolName,
			CallID:    cloneCallID(decisionInput.CallID),
			Reason:    cloneString(decisionInput.Reason),
		},
		conversation,
	)
	decidedDraft, err := session.NewEventDraft(
		DecidedEvent, Decided{
			ID:      identifier,
			Outcome: decisionOutcome,
		},
	)
	if err != nil {
		return "", err
	}
	if _, err = conversation.Commit(
		context.WithoutCancel(ctx),
		session.Batch(decidedDraft),
	); err != nil {
		return "", err
	}
	return decisionOutcome, nil
}

func (owner *Service) decide(ctx context.Context, input Question, conversation session.Context) Outcome {
	if ctx.Err() != nil {
		return OutcomeCancelled
	}
	selectedPolicy, err := owner.EffectivePolicy(conversation)
	if err != nil {
		return OutcomeUnavailable
	}
	if selectedPolicy == PolicyNever {
		return OutcomeRejected
	}
	outcome := owner.answer(ctx, input)
	if ctx.Err() != nil {
		return OutcomeCancelled
	}
	return outcome
}

func (owner *Service) answer(ctx context.Context, input Question) (outcome Outcome) {
	outcome = OutcomeUnavailable
	if owner.answerer == nil {
		return outcome
	}
	defer func() {
		if recover() != nil {
			outcome = OutcomeUnavailable
		}
	}()
	resolved, err := owner.answerer.Answer(ctx, input)
	if err != nil || !validOutcome(resolved) {
		return OutcomeUnavailable
	}
	return resolved
}

func hasOpenTurn(entries []session.Event) bool {
	for index := len(entries) - 1; index >= 0; index-- {
		switch entries[index].Type {
		case session.TurnStartEventName:
			return true
		case session.TurnEndEventName:
			return false
		}
	}
	return false
}

func mintRequestID() (RequestID, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return RequestID(fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		raw[0:4],
		raw[4:6],
		raw[6:8],
		raw[8:10],
		raw[10:16],
	)), nil
}
