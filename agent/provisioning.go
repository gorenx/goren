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
