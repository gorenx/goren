package subagent

import (
	"context"

	"github.com/gorenx/goren/session"
)

// SeedBuilder constructs the detached Session prefix used only when a child
// Session is created for the first time. It does not create or run an Agent.
type SeedBuilder interface {
	Name() string
	ContextPolicy() ParentContextPolicy
	BuildSeed(context.Context, []session.Event) (SessionSeed, error)
}

// ParentContextPolicy identifies which parent events may enter a child seed.
type ParentContextPolicy uint8

const (
	// NoParentContext creates a child without copied parent events.
	NoParentContext ParentContextPolicy = iota
	// CompletedParentTurns copies only the completed prefix of the parent log.
	CompletedParentTurns
)

// SessionSeed is the immutable event prefix contributed by a SeedBuilder.
type SessionSeed struct {
	events []session.Event
}

// NewSessionSeed snapshots one event prefix.
func NewSessionSeed(events []session.Event) SessionSeed {
	return SessionSeed{
		events: cloneSeedEvents(events),
	}
}

// EventPrefix returns a detached copy of the seed events.
func (seed SessionSeed) EventPrefix() []session.Event {
	return cloneSeedEvents(seed.events)
}

func cloneSeedEvents(source []session.Event) []session.Event {
	if source == nil {
		return nil
	}
	detached := make([]session.Event, len(source))
	for index, eventValue := range source {
		detached[index] = eventValue
		detached[index].Data = append([]byte(nil), eventValue.Data...)
		if eventValue.SourceEventSeqs != nil {
			sequences := append([]int64(nil), (*eventValue.SourceEventSeqs)...)
			detached[index].SourceEventSeqs = &sequences
		}
		if eventValue.SurfaceOp != nil {
			operation := *eventValue.SurfaceOp
			detached[index].SurfaceOp = &operation
		}
	}
	return detached
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
