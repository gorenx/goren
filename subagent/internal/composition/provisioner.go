package composition

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
)

// provisioner gives multiple child contributors one atomic provisioning call.
type provisioner struct {
	parts []agent.Provisioner
}

func (owner *provisioner) Provision(
	requestContext context.Context,
	scope agent.Scope,
) (agent.Provisioning, error) {
	if owner == nil {
		return nil, errors.New("subagent: child Provisioner is unavailable")
	}
	acquired := make([]agent.Provisioning, 0, len(owner.parts))
	for _, part := range owner.parts {
		if part == nil {
			return nil, errors.Join(
				errors.New("subagent: child Provisioner contains a nil part"),
				disposeProvisionings(
					context.WithoutCancel(requestContext),
					acquired,
				),
			)
		}
		result, provisionErr := part.Provision(requestContext, scope)
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
	return &provisioning{
		parts: acquired,
	}, nil
}

var _ agent.Provisioner = (*provisioner)(nil)
