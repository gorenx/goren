package continuable

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/scopedplugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/childpolicy"
	extensionregistry "github.com/gorenx/goren/subagent/internal/extension"
)

type scopePolicy struct {
	childID    session.SessionID
	parentID   session.SessionID
	descriptor subagent.ContinuableDescriptor
	fresh      bool
}

func (owner *Service) provisioner(policy scopePolicy) agent.Provisioner {
	parts := make([]agent.Provisioner, 0, 2)
	instances := childpolicy.Plugins(
		childpolicy.PolicySet{
			Delegation:      owner.dependencies.Approval,
			SeedDelegation:  policy.fresh,
			Persona:         policy.descriptor.Persona,
			ToolRestriction: policy.descriptor.ToolFilter,
		},
	)
	if len(instances) != 0 {
		parts = append(parts, scopedplugin.MountPlugins(instances...))
	}
	if owner.dependencies.Extensions != nil {
		parts = append(
			parts,
			extensionregistry.NewProvisioner(
				owner.dependencies.Extensions,
				extensionregistry.Input{
					ChildID:    policy.childID,
					ParentID:   policy.parentID,
					Descriptor: policy.descriptor,
				},
			),
		)
	}
	if len(parts) == 0 {
		return nil
	}
	return &compositeProvisioner{
		parts: parts,
	}
}

type compositeProvisioner struct {
	parts []agent.Provisioner
}

func (owner *compositeProvisioner) Provision(
	requestContext context.Context,
	target agent.Scope,
) (agent.Provisioning, error) {
	acquired := make([]agent.Provisioning, 0, len(owner.parts))
	for _, part := range owner.parts {
		if part == nil {
			return nil, errors.Join(
				errors.New("subagent: Continuable Provisioner contains a nil part"),
				disposeProvisionings(
					context.WithoutCancel(requestContext),
					acquired,
				),
			)
		}
		result, provisionErr := part.Provision(requestContext, target)
		if provisionErr != nil {
			return nil, errors.Join(
				provisionErr,
				disposeProvisionings(
					context.WithoutCancel(requestContext),
					acquired,
				),
			)
		}
		if result != nil {
			acquired = append(acquired, result)
		}
	}
	if len(acquired) == 0 {
		return nil, nil
	}
	return &compositeProvisioning{
		parts: acquired,
	}, nil
}

type compositeProvisioning struct {
	mutex  sync.Mutex
	parts  []agent.Provisioning
	closed bool
}

func (acquired *compositeProvisioning) Commit() error {
	acquired.mutex.Lock()
	defer acquired.mutex.Unlock()
	if acquired.closed {
		return errors.New("subagent: Continuable Provisioning is closed")
	}
	for _, part := range acquired.parts {
		if commitErr := part.Commit(); commitErr != nil {
			return commitErr
		}
	}
	return nil
}

func (acquired *compositeProvisioning) Dispose(closeContext context.Context) error {
	if acquired == nil {
		return nil
	}
	acquired.mutex.Lock()
	if acquired.closed {
		acquired.mutex.Unlock()
		return nil
	}
	acquired.closed = true
	parts := append([]agent.Provisioning(nil), acquired.parts...)
	acquired.mutex.Unlock()
	return disposeProvisionings(closeContext, parts)
}

func disposeProvisionings(
	closeContext context.Context,
	parts []agent.Provisioning,
) error {
	var closeErr error
	for index := len(parts) - 1; index >= 0; index-- {
		closeErr = errors.Join(closeErr, parts[index].Dispose(closeContext))
	}
	return closeErr
}

var _ agent.Provisioner = (*compositeProvisioner)(nil)
var _ agent.Provisioning = (*compositeProvisioning)(nil)
