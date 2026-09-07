package approval

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

func effectiveOverride(conversation session.Context) (Policy, bool, error) {
	if conversation == nil {
		return "", false, errors.New("approval: Session is nil")
	}
	return policyOverride(conversation.Events())
}

func appendPolicy(
	requestContext context.Context,
	conversation session.Context,
	selectedPolicy Policy,
	source *PolicySource,
) error {
	if !validPolicy(selectedPolicy) {
		return errors.New("approval: policy must be ask or never")
	}
	if source != nil && *source != PolicySourceDelegation {
		return errors.New("approval: policy source must be delegation")
	}
	draft, err := session.NewEventDraft(PolicyEvent, PolicyChanged{
		Policy: selectedPolicy,
		Source: source,
	})
	if err != nil {
		return err
	}
	_, err = conversation.Commit(requestContext, session.Batch(draft))
	return err
}

// SeedDelegationPolicy pins one unpublished delegated Session to never ask for
// approval. The event is model-reconstructable and carries delegation rather
// than user attribution.
func (*Service) SeedDelegationPolicy(
	requestContext context.Context,
	conversation session.Context,
) error {
	if conversation == nil {
		return errors.New("approval: delegated Session is nil")
	}
	source := PolicySourceDelegation
	return appendPolicy(requestContext, conversation, PolicyNever, &source)
}

func policyNotice(previous Policy, selectedPolicy Policy) (agentmessage.UserMessage, error) {
	return agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock(fmt.Sprintf(
			"The approval policy changed from %q to %q (changed by the user).", previous, selectedPolicy,
		))},
		Source: agentmessage.PluginMessageSource{
			Plugin: "user-approval",
		},
	})
}

func (owner *Service) EffectivePolicy(conversation session.Context) (Policy, error) {
	if conversation == nil {
		return "", errors.New("approval: Session is nil")
	}
	return owner.defaultPolicy.Resolve(conversation.Events())
}

func (*Service) OverrideOf(conversation session.Context) (Policy, bool, error) {
	return effectiveOverride(conversation)
}

func (owner *Service) SetPolicy(
	requestContext context.Context,
	agentSubject ApprovalTarget,
	selectedPolicy Policy,
) error {
	if requestContext == nil || agentSubject == nil || agentSubject.SessionValue() == nil {
		return errors.New("approval: Context, Subject, and Session are required")
	}
	if err := requestContext.Err(); err != nil {
		return err
	}
	previous, err := owner.EffectivePolicy(agentSubject.SessionValue())
	if err != nil {
		return err
	}
	if previous == selectedPolicy {
		return nil
	}
	if err = appendPolicy(
		requestContext,
		agentSubject.SessionValue(),
		selectedPolicy,
		nil,
	); err != nil {
		return err
	}
	messageValue, err := policyNotice(previous, selectedPolicy)
	if err != nil {
		return err
	}
	return agentSubject.Inject(messageValue)
}
