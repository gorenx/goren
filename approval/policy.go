package approval

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/session"
)

const (
	askPolicyText   = "Approval policy: ask. Operations that require approval may ask through the configured answerers; without an available answerer, the request fails closed."
	neverPolicyText = "Approval prompts are disabled in this session: actions that require approval are rejected automatically — do not request sandbox escalation (do not set `sandbox_permissions`)."
)

func policyOverride(entries []session.Event) (Policy, bool, error) {
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

// Resolve selects the last Session override over this default policy.
func (fallback Policy) Resolve(entries []session.Event) (Policy, error) {
	if !validPolicy(fallback) {
		return "", errors.New("approval: default policy must be ask or never")
	}
	selectedPolicy, found, err := policyOverride(entries)
	if err != nil {
		return "", err
	}
	if found {
		return selectedPolicy, nil
	}
	return fallback, nil
}

// Text renders the already-resolved policy without accessing Session or Prompt.
func (selectedPolicy Policy) Text() (string, error) {
	switch selectedPolicy {
	case PolicyAsk:
		return askPolicyText, nil
	case PolicyNever:
		return neverPolicyText, nil
	default:
		return "", errors.New("approval: policy must be ask or never")
	}
}
