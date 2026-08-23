package approval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

const (
	askPolicyText   = "Approval policy: ask. Operations that require approval may ask through the configured answerers; without an available answerer, the request fails closed."
	neverPolicyText = "Approval prompts are disabled in this session: actions that require approval are rejected automatically — do not request sandbox escalation (do not set `sandbox_permissions`)."
)

func effectiveOverride(conversation *session.Session) (Policy, bool, error) {
	if conversation == nil {
		return "", false, errors.New("approval: Session is nil")
	}
	entries := conversation.Events()
	for index := len(entries) - 1; index >= 0; index-- {
		committed := entries[index]
		if committed.Type != PolicyEventName {
			continue
		}
		var change PolicyChanged
		if err := json.Unmarshal(committed.Data, &change); err != nil {
			return "", false, fmt.Errorf("approval: invalid policy event at seq %d: %w", committed.Seq, err)
		}
		if !validPolicy(change.Policy) || (change.Source != nil && *change.Source != PolicySourceDelegation) {
			return "", false, fmt.Errorf("approval: invalid policy event at seq %d", committed.Seq)
		}
		return change.Policy, true, nil
	}
	return "", false, nil
}

func appendPolicy(conversation *session.Session, selectedPolicy Policy, source *PolicySource) error {
	if !validPolicy(selectedPolicy) {
		return errors.New("approval: policy must be ask or never")
	}
	if source != nil && *source != PolicySourceDelegation {
		return errors.New("approval: policy source must be delegation")
	}
	_, err := session.AppendSerialized(conversation, PolicyEvent, PolicyChanged{
		Policy: selectedPolicy,
		Source: source,
	})
	return err
}

// SeedDelegationPolicy pins one unpublished delegated Session to never ask for
// approval. The event is model-reconstructable and carries delegation rather
// than user attribution.
func (*Service) SeedDelegationPolicy(conversation *session.Session) error {
	if conversation == nil {
		return errors.New("approval: delegated Session is nil")
	}
	source := PolicySourceDelegation
	return appendPolicy(conversation, PolicyNever, &source)
}

func policyNotice(previous Policy, selectedPolicy Policy) (llm.UserMessage, error) {
	return llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{llm.NewTextBlock(fmt.Sprintf(
			"The approval policy changed from %q to %q (changed by the user).", previous, selectedPolicy,
		))},
		Source: llm.PluginMessageSource{
			Plugin: "user-approval",
		},
	})
}

func (owner *Service) EffectivePolicy(conversation *session.Session) (Policy, error) {
	selectedPolicy, found, err := effectiveOverride(conversation)
	if err != nil {
		return "", err
	}
	if found {
		return selectedPolicy, nil
	}
	if owner.parent != nil {
		return owner.parent.EffectivePolicy(conversation)
	}
	return owner.defaultPolicy, nil
}

func (*Service) OverrideOf(conversation *session.Session) (Policy, bool, error) {
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
	if err := appendPolicy(agentSubject.SessionValue(), selectedPolicy, nil); err != nil {
		return err
	}
	messageValue, err := policyNotice(previous, selectedPolicy)
	if err != nil {
		return err
	}
	return agentSubject.Inject(messageValue)
}
