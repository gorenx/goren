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

// Policy declares that fork copies completed parent turns.
func (*Builder) Policy() subagent.SeedPolicy {
	return subagent.SeedPolicy{
		ParentContext: subagent.CompletedParentTurns,
	}
}

// BuildSeed returns the detached prefix ending at the last completed turn.
func (*Builder) BuildSeed(
	requestContext context.Context,
	request subagent.SeedRequest,
) (subagent.SessionSeed, error) {
	if requestContext == nil {
		return subagent.SessionSeed{}, errors.New(
			"subagent: fork seed context is nil",
		)
	}
	if requestErr := requestContext.Err(); requestErr != nil {
		return subagent.SessionSeed{}, requestErr
	}
	return subagent.SessionSeed{
		Events: completedTurnPrefix(request.Parent.Events),
	}, nil
}

func completedTurnPrefix(events []session.Event) []session.Event {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type == session.TurnEndEventName {
			return cloneEvents(events[:index+1])
		}
	}
	return nil
}

func cloneEvents(source []session.Event) []session.Event {
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
