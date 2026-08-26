package subagent

import (
	"context"

	"github.com/gorenx/goren/session"
)

// SeedBuilder constructs the detached Session prefix used only when a child
// Session is created for the first time. It does not create or run an Agent.
type SeedBuilder interface {
	Name() string
	Policy() SeedPolicy
	BuildSeed(context.Context, SeedRequest) (SessionSeed, error)
}

// SeedPolicy describes the durable input selected by a SeedBuilder.
type SeedPolicy struct {
	ParentContext ParentContextPolicy
}

// ParentContextPolicy identifies which parent events may enter a child seed.
type ParentContextPolicy uint8

const (
	// NoParentContext creates a child without copied parent events.
	NoParentContext ParentContextPolicy = iota
	// CompletedParentTurns copies only the completed prefix of the parent log.
	CompletedParentTurns
)

// SeedRequest contains immutable identities and a detached parent snapshot.
type SeedRequest struct {
	ChildID session.SessionID
	Parent  ParentSnapshot
}

// ParentSnapshot is the detached parent Session state visible to a
// SeedBuilder. The live parent Agent is intentionally not exposed.
type ParentSnapshot struct {
	SessionID session.SessionID
	Header    session.Header
	Events    []session.Event
}

// SessionSeed is the detached event prefix contributed by a SeedBuilder.
type SessionSeed struct {
	Events []session.Event
}

// SeedBuilderRegistration owns one exact SeedBuilder registration.
type SeedBuilderRegistration interface {
	Unregister(context.Context) error
}

// SeedBuilderRegistry owns registration and exact lookup.
type SeedBuilderRegistry interface {
	Register(context.Context, SeedBuilder) (SeedBuilderRegistration, error)
	Find(string) (SeedBuilder, bool)
}
