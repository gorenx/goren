// Package fork provides the parent-prefix child Session seed strategy.
package fork

import (
	"context"
	"errors"
	"strings"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

const (
	// PluginName is the canonical fork Plugin name.
	PluginName = "@deepseek-ai/dsh-subagent-fork-in-process"
	// DefaultSeedBuilderName is the canonical external registry identity.
	DefaultSeedBuilderName = "fork"
)

// Builder copies the completed prefix of the parent Session log.
type Builder struct {
	name string
}

func newBuilder(builderName string) (*Builder, error) {
	if err := validateBuilderName(builderName); err != nil {
		return nil, err
	}
	return &Builder{
		name: builderName,
	}, nil
}

// Name returns the exact SeedBuilder registry identity.
func (owner *Builder) Name() string {
	return owner.name
}

// ContextPolicy declares that fork copies completed parent turns.
func (*Builder) ContextPolicy() subagent.ParentContextPolicy {
	return subagent.CompletedParentTurns
}

// BuildSeed returns the detached prefix ending at the last completed turn.
func (*Builder) BuildSeed(
	requestContext context.Context,
	parentEvents []session.Event,
) (subagent.SessionSeed, error) {
	if requestContext == nil {
		return subagent.SessionSeed{}, errors.New(
			"subagent: fork seed context is nil",
		)
	}
	if requestErr := requestContext.Err(); requestErr != nil {
		return subagent.SessionSeed{}, requestErr
	}
	return subagent.NewSessionSeed(completedTurnPrefix(parentEvents)), nil
}

func completedTurnPrefix(events []session.Event) []session.Event {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type == session.TurnEndEventName {
			return events[:index+1]
		}
	}
	return nil
}

func validateBuilderName(builderName string) error {
	if strings.TrimSpace(builderName) == "" ||
		builderName != strings.TrimSpace(builderName) {
		return errors.New(
			"subagent: fork SeedBuilder name must be non-empty and trimmed",
		)
	}
	return nil
}

var _ subagent.SeedBuilder = (*Builder)(nil)
