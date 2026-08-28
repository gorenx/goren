package plugin

import (
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent/internal/bound"
	extensionregistry "github.com/gorenx/goren/subagent/internal/extension"
)

// boundExtensions adapts the generic Extension Registry to Bound's
// consumer-owned per-epoch provisioning contract.
type boundExtensions struct {
	registry *extensionregistry.Registry
}

func (adapter boundExtensions) Provision(
	names []string,
) (agent.Provisioner, error) {
	return extensionregistry.NewSelectedProvisioner(
		adapter.registry,
		names,
	)
}

var _ bound.Extensions = boundExtensions{}
