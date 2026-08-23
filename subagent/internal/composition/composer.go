// Package composition builds the unpublished Agent Setup for one continuable
// child without owning continuation admission or residency.
package composition

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/subagent/internal/continuation"
	setupregistry "github.com/gorenx/goren/subagent/internal/setup"
)

// Composer owns the deployment capabilities installed in a child Scope.
type Composer struct {
	approval approval.DelegationPolicy
	setups   *setupregistry.Registry
}

// New constructs a child Composer from optional owner-defined capabilities.
func New(
	approvalPolicy approval.DelegationPolicy,
	setups *setupregistry.Registry,
) *Composer {
	return &Composer{
		approval: approvalPolicy,
		setups:   setups,
	}
}

// Compose builds a fresh Setup for one materialization. Delegation approval is
// seeded only for fresh Sessions; cold resume replays the durable policy.
func (owner *Composer) Compose(
	input continuation.Composition,
) agent.Setup {
	if owner == nil {
		return nil
	}
	parts := make([]agent.Setup, 0, 2)
	instances := owner.buildPlugins(input)
	if len(instances) != 0 {
		parts = append(
			parts,
			agent.MountPlugins(instances...),
		)
	}
	if owner.setups != nil {
		parts = append(
			parts,
			owner.setups.Compose(
				setupregistry.Input{
					ChildID:    input.ChildID,
					ParentID:   input.ParentID,
					Descriptor: input.Descriptor,
				},
			),
		)
	}
	if len(parts) == 0 {
		return nil
	}
	return &composition{
		parts: parts,
	}
}

func (owner *Composer) buildPlugins(
	input continuation.Composition,
) []plugin.Plugin {
	instances := make([]plugin.Plugin, 0, 3)
	if owner.approval != nil && input.Fresh {
		instances = append(
			instances,
			&delegationPolicy{
				policy: owner.approval,
			},
		)
	}
	if input.Descriptor.Persona != nil {
		instances = append(
			instances,
			newPersona(*input.Descriptor.Persona),
		)
	}
	if input.Descriptor.ToolFilter != nil {
		instances = append(
			instances,
			newToolRestriction(*input.Descriptor.ToolFilter),
		)
	}
	return instances
}

// composition gives multiple domain contributors one Agent Setup lifecycle.
type composition struct {
	mutex    sync.Mutex
	parts    []agent.Setup
	prepared int
	closed   bool
}

func (setup *composition) Prepare(
	requestContext context.Context,
	scope agent.Scope,
) error {
	for index, part := range setup.parts {
		setup.prepared = index + 1
		if prepareErr := part.Prepare(requestContext, scope); prepareErr != nil {
			return prepareErr
		}
	}
	return nil
}

func (setup *composition) Commit() error {
	for index := 0; index < setup.prepared; index++ {
		if commitErr := setup.parts[index].Commit(); commitErr != nil {
			return commitErr
		}
	}
	return nil
}

func (setup *composition) Dispose(closeContext context.Context) error {
	setup.mutex.Lock()
	if setup.closed {
		setup.mutex.Unlock()
		return nil
	}
	setup.closed = true
	prepared := setup.prepared
	setup.mutex.Unlock()
	var closeErr error
	for index := prepared - 1; index >= 0; index-- {
		closeErr = errors.Join(
			closeErr,
			setup.parts[index].Dispose(closeContext),
		)
	}
	return closeErr
}

var _ continuation.Composer = (*Composer)(nil)
var _ agent.Setup = (*composition)(nil)
