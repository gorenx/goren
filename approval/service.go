package approval

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

// RuntimeOptions supplies injectable identity generation for deterministic tests.
type RuntimeOptions struct {
	NewRequestID func() (RequestID, error)
}

type approvalService struct {
	sourceScope   *plugin.Scope
	defaultPolicy Policy
	newRequestID  func() (RequestID, error)
}

// New creates the Approval service and contributes its cache-safe policy context.
func New(
	requestContext context.Context,
	sourceScope *plugin.Scope,
	promptService systemprompt.SystemPrompt,
	settings ValidatedConfig,
	options RuntimeOptions,
) (Approval, error) {
	if requestContext == nil || sourceScope == nil || promptService == nil {
		return nil, errors.New("approval: Context, Scope, and System Prompt are required")
	}
	if !validPolicy(settings.policy) {
		return nil, errors.New("approval: configuration was not validated")
	}
	newRequestID := options.NewRequestID
	if newRequestID == nil {
		newRequestID = mintRequestID
	}
	owner := &approvalService{
		sourceScope: sourceScope, defaultPolicy: settings.policy, newRequestID: newRequestID,
	}
	if _, err := promptService.Context(requestContext, sourceScope, systemprompt.PromptContext{
		Name: "approval:policy", Order: 115,
		Text: systemprompt.TextFunc(func(_ context.Context, assemblyContext systemprompt.AssembleContext) (string, error) {
			if assemblyContext.Session == nil {
				return "", nil
			}
			selectedPolicy, err := owner.EffectivePolicy(assemblyContext.Session)
			if err != nil {
				return "", err
			}
			if selectedPolicy == PolicyNever {
				return neverPolicyText, nil
			}
			return askPolicyText, nil
		}),
	}); err != nil {
		return nil, err
	}
	return owner, nil
}

func (owner *approvalService) Request(requestContext context.Context, decisionRequest Request) (Outcome, error) {
	if requestContext == nil || decisionRequest.Subject == nil || decisionRequest.Subject.SessionValue() == nil {
		return "", errors.New("approval: Context, Agent, and Session are required")
	}
	conversation := decisionRequest.Subject.SessionValue()
	if !hasOpenTurn(conversation.Events()) {
		return "", errors.New("approval: request outside an open turn; audit events must be turn-enclosed")
	}
	identifier, err := owner.newRequestID()
	if err != nil {
		return "", fmt.Errorf("approval: mint request id: %w", err)
	}
	if identifier == "" {
		return "", errors.New("approval: minted request id is empty")
	}
	if _, err := session.Append(conversation, AskedEvent, Asked{
		ID: identifier, ToolName: decisionRequest.ToolName,
		CallID: cloneCallID(decisionRequest.CallID), Reason: cloneString(decisionRequest.Reason),
	}); err != nil {
		return "", err
	}
	decisionOutcome := owner.decide(requestContext, decisionRequest, conversation)
	if _, err := session.Append(conversation, DecidedEvent, Decided{ID: identifier, Outcome: decisionOutcome}); err != nil {
		return "", err
	}
	return decisionOutcome, nil
}

func (owner *approvalService) decide(
	requestContext context.Context,
	decisionRequest Request,
	conversation *session.Session,
) Outcome {
	if requestContext.Err() != nil {
		return OutcomeCancelled
	}
	selectedPolicy, err := owner.EffectivePolicy(conversation)
	if err != nil {
		return OutcomeUnavailable
	}
	if selectedPolicy == PolicyNever {
		return OutcomeRejected
	}
	type result struct {
		outcome Outcome
		err     error
	}
	settled := make(chan result, 1)
	go func() {
		decisionOutcome, dispatchErr := plugin.WaterfallScopedFrom(
			requestContext, owner.sourceScope, decisionRequest.Subject.ScopeValue().Target(), requestEvent, decisionRequest,
			func(context.Context, Request) (Outcome, error) { return OutcomeUnavailable, nil },
		)
		settled <- result{outcome: decisionOutcome, err: dispatchErr}
	}()
	select {
	case <-requestContext.Done():
		return OutcomeCancelled
	case completed := <-settled:
		if completed.err != nil || !validOutcome(completed.outcome) {
			return OutcomeUnavailable
		}
		return completed.outcome
	}
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
		raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16],
	)), nil
}
