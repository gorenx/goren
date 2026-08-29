package agent

import (
	"context"
	"errors"
)

// ScopeResource is one exact idempotent resource owned by an Agent Scope.
// It may be disposed early; later Scope teardown must remain safe.
type ScopeResource interface {
	Dispose(context.Context) error
}

// Scope is the Plugin-neutral resource boundary of one exact Agent. Concrete
// adapters may offer additional installation capabilities in their own
// packages, but Agent business contracts do not accept Plugin instances.
type Scope interface {
	Agent() Agent
	Own(ScopeResource) error
}

// Provisioner configures one unpublished or live Agent Scope. A failed call
// must release every resource it has not transferred to the Scope.
type Provisioner interface {
	Provision(context.Context, Scope) (Provisioning, error)
}

// Provisioning owns resources that survive successful provisioning. Commit
// validates the publication boundary; Dispose releases the resources during
// rollback or Agent Scope teardown.
type Provisioning interface {
	ScopeResource
	Commit() error
}

// ApplyProvisioning executes the single Agent Scope provisioning transaction
// used by both construction-time and live extension installation.
func ApplyProvisioning(
	requestContext context.Context,
	target Scope,
	source Provisioner,
) error {
	if target == nil || source == nil {
		return errors.New("agent: Scope and Provisioner are required")
	}
	acquired, err := source.Provision(requestContext, target)
	if err != nil || acquired == nil {
		return err
	}
	if err = target.Own(acquired); err != nil {
		return errors.Join(
			err,
			acquired.Dispose(context.WithoutCancel(requestContext)),
		)
	}
	return acquired.Commit()
}

// ComposeProvisioners returns one Provisioner that applies its non-nil
// sources in order and owns their returned Provisioning values as one
// publication transaction. A failed source disposes earlier acquisitions in
// reverse order; Agent Scope resources already transferred by a source remain
// owned by that Scope.
func ComposeProvisioners(sources ...Provisioner) Provisioner {
	detached := make([]Provisioner, 0, len(sources))
	for _, source := range sources {
		if source != nil {
			detached = append(detached, source)
		}
	}
	if len(detached) == 0 {
		return nil
	}
	return &composedProvisioner{
		sources: detached,
	}
}

type composedProvisioner struct {
	sources []Provisioner
}

func (owner *composedProvisioner) Provision(
	requestContext context.Context,
	target Scope,
) (Provisioning, error) {
	if owner == nil || target == nil {
		return nil, errors.New("agent: composed Provisioner is unavailable")
	}
	acquired := make([]Provisioning, 0, len(owner.sources))
	for _, source := range owner.sources {
		acquiredProvisioning, err := source.Provision(requestContext, target)
		if err != nil {
			return nil, errors.Join(
				err,
				disposeProvisionings(
					context.WithoutCancel(requestContext),
					acquired,
				),
			)
		}
		if acquiredProvisioning != nil {
			acquired = append(acquired, acquiredProvisioning)
		}
	}
	if len(acquired) == 0 {
		return nil, nil
	}
	return &composedProvisioning{
		acquired: acquired,
	}, nil
}

type composedProvisioning struct {
	acquired []Provisioning
}

func (owner *composedProvisioning) Commit() error {
	if owner == nil {
		return nil
	}
	for _, acquiredProvisioning := range owner.acquired {
		if err := acquiredProvisioning.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (owner *composedProvisioning) Dispose(closeContext context.Context) error {
	if owner == nil {
		return nil
	}
	return disposeProvisionings(closeContext, owner.acquired)
}

func disposeProvisionings(
	closeContext context.Context,
	acquired []Provisioning,
) error {
	var closeErr error
	for index := len(acquired) - 1; index >= 0; index-- {
		closeErr = errors.Join(
			closeErr,
			acquired[index].Dispose(closeContext),
		)
	}
	return closeErr
}

var _ Provisioner = (*composedProvisioner)(nil)
var _ Provisioning = (*composedProvisioning)(nil)
