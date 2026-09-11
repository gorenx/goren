package agent

import (
	"context"
	"errors"
	"github.com/gorenx/goren/session"
)

type initiatorContextKey struct{}

type initiatorContextValue struct {
	identifier session.SessionID
	defined    bool
}

// WithInitiator returns a derived context carrying explicit identity-only causal
// attribution. It does not resolve identity or grant authorization.
func WithInitiator(requestContext context.Context, identifier session.SessionID) (context.Context, error) {
	if requestContext == nil || identifier == "" {
		return nil, errors.New("agent: initiator Context and Agent ID are required")
	}
	return context.WithValue(
		requestContext,
		initiatorContextKey{},
		initiatorContextValue{
			identifier: identifier,
			defined:    true,
		},
	), nil
}

// WithoutInitiator explicitly hides an inherited initiator for shared work.
func WithoutInitiator(requestContext context.Context) (context.Context, error) {
	if requestContext == nil {
		return nil, errors.New("agent: initiator Context is nil")
	}
	return context.WithValue(
		requestContext,
		initiatorContextKey{},
		initiatorContextValue{
			defined: true,
		},
	), nil
}

// InitiatorFrom reads optional causal attribution from one explicit call chain.
func InitiatorFrom(requestContext context.Context) (session.SessionID, bool) {
	if requestContext == nil {
		return "", false
	}
	value, found := requestContext.Value(initiatorContextKey{}).(initiatorContextValue)
	if !found || !value.defined || value.identifier == "" {
		return "", false
	}
	return value.identifier, true
}

// RequireInitiator rejects an Agent-less call chain.
func RequireInitiator(requestContext context.Context) (session.SessionID, error) {
	identifier, found := InitiatorFrom(requestContext)
	if !found {
		return "", errors.New("agent: no initiating Agent is active")
	}
	return identifier, nil
}
