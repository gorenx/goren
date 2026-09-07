package approval

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/session"
)

// Service prepares approval decisions from fixed facts without executing effects.
type Service struct {
	defaultPolicy Policy
}

// New constructs independently configured approval rules.
func New(settings ValidatedConfig) (*Service, error) {
	if !validPolicy(settings.policy) {
		return nil, errors.New("approval: configuration was not validated")
	}
	return &Service{
		defaultPolicy: settings.policy,
	}, nil
}

// Prepare validates the turn and fixes the policy and audit identity for this request.
func (owner *Service) Prepare(input Request) (*Decision, error) {
	if input.SessionID == "" || strings.TrimSpace(input.ToolName) == "" {
		return nil, errors.New("approval: Session identity and tool name are required")
	}
	if !hasOpenTurn(input.Events) {
		return nil, errors.New("approval: request outside an open turn; audit events must be turn-enclosed")
	}
	selectedPolicy, err := owner.defaultPolicy.Resolve(input.Events)
	if err != nil {
		return nil, err
	}
	identifier, err := mintRequestID()
	if err != nil {
		return nil, fmt.Errorf("approval: mint request id: %w", err)
	}
	return &Decision{
		policy: selectedPolicy,
		question: Question{
			ID:        identifier,
			SessionID: input.SessionID,
			ToolName:  input.ToolName,
			CallID:    cloneCallID(input.CallID),
			Reason:    cloneString(input.Reason),
		},
	}, nil
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
