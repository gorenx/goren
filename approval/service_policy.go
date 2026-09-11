package approval

import (
	"errors"
	"fmt"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

// PolicyChange carries an audit draft and the message to inject after its commit.
type PolicyChange struct {
	Draft  session.EventDraft
	Notice agentmessage.UserMessage
}

// ChangePolicy prepares a user change; nil means the effective policy already matches.
func (owner *Service) ChangePolicy(entries []session.Event, selectedPolicy Policy) (*PolicyChange, error) {
	if !validPolicy(selectedPolicy) {
		return nil, errors.New("approval: policy must be ask or never")
	}
	previous, err := owner.defaultPolicy.Resolve(entries)
	if err != nil {
		return nil, err
	}
	if previous == selectedPolicy {
		return nil, nil
	}
	draft, err := session.NewEventDraft(PolicyEvent, PolicyChanged{
		Policy: selectedPolicy,
	})
	if err != nil {
		return nil, err
	}
	messageValue, err := policyNotice(previous, selectedPolicy)
	if err != nil {
		return nil, err
	}
	return &PolicyChange{
		Draft:  draft,
		Notice: messageValue,
	}, nil
}

// DelegationPolicyDraft pins delegated history to never ask, with explicit attribution.
func DelegationPolicyDraft() (session.EventDraft, error) {
	source := PolicySourceDelegation
	return session.NewEventDraft(PolicyEvent, PolicyChanged{
		Policy: PolicyNever,
		Source: &source,
	})
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
