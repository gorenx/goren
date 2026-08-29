// Package spawn provides the fresh child Session seed strategy.
package spawn

import (
	"context"
	"errors"
	"strings"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

const (
	// PluginName is the canonical spawn Plugin name.
	PluginName = "@deepseek-ai/dsh-subagent-spawn-in-process"
	// DefaultSeedBuilderName is the canonical external registry identity.
	DefaultSeedBuilderName = "spawn"
)

// Builder creates an empty child Session seed.
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

// ContextPolicy declares that spawn does not copy parent conversation events.
func (*Builder) ContextPolicy() subagent.ParentContextPolicy {
	return subagent.NoParentContext
}

// BuildSeed returns an empty detached seed.
func (*Builder) BuildSeed(
	requestContext context.Context,
	_ []session.Event,
) (subagent.SessionSeed, error) {
	if requestContext == nil {
		return subagent.SessionSeed{}, errors.New(
			"subagent: spawn seed context is nil",
		)
	}
	return subagent.SessionSeed{}, requestContext.Err()
}

func validateBuilderName(builderName string) error {
	if strings.TrimSpace(builderName) == "" ||
		builderName != strings.TrimSpace(builderName) {
		return errors.New(
			"subagent: spawn SeedBuilder name must be non-empty and trimmed",
		)
	}
	return nil
}

var _ subagent.SeedBuilder = (*Builder)(nil)
