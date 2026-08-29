package agent

import (
	"context"
	"errors"
)

type initiatorContextKey struct{}

type initiatorContextValue struct {
	subject Agent
	defined bool
}

// WithInitiator returns a derived context carrying explicit same-process causal
// attribution. It does not resolve identity or grant authorization.
func WithInitiator(requestContext context.Context, subject Agent) (context.Context, error) {
	if requestContext == nil || subject == nil {
		return nil, errors.New("agent: initiator Context and Agent are required")
	}
	return context.WithValue(
		requestContext,
		initiatorContextKey{},
		initiatorContextValue{
			subject: subject,
			defined: true,
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
func InitiatorFrom(requestContext context.Context) (Agent, bool) {
	if requestContext == nil {
		return nil, false
	}
	value, found := requestContext.Value(initiatorContextKey{}).(initiatorContextValue)
	if !found || !value.defined || value.subject == nil {
		return nil, false
	}
	return value.subject, true
}

// RequireInitiator rejects an Agent-less call chain.
func RequireInitiator(requestContext context.Context) (Agent, error) {
	subject, found := InitiatorFrom(requestContext)
	if !found {
		return nil, errors.New("agent: no initiating Agent is active")
	}
	return subject, nil
}
