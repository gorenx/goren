package composition

import (
	"context"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/tools"
)

const restrictionName = "subagent"

type toolRestriction struct {
	plugin.Base
	restriction tools.ToolRestriction
	handle      *tools.RestrictionHandle
}

func newToolRestriction(restriction tools.ToolRestriction) *toolRestriction {
	return &toolRestriction{
		restriction: restriction,
	}
}

func (*toolRestriction) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "@goren/subagent/tool-restriction",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[tools.PolicyRegistry](),
		},
	}
}

func (restriction *toolRestriction) Apply(
	requestContext context.Context,
) error {
	policies, requireErr := plugin.Require[tools.PolicyRegistry](restriction)
	if requireErr != nil {
		return requireErr
	}
	handle, addErr := policies.AddRestriction(
		requestContext,
		restrictionName,
		restriction.restriction,
	)
	if addErr != nil {
		return addErr
	}
	restriction.handle = handle
	return nil
}

func (restriction *toolRestriction) Dispose(
	closeContext context.Context,
) error {
	if restriction.handle == nil {
		return nil
	}
	closeErr := restriction.handle.Unregister(closeContext)
	restriction.handle = nil
	return closeErr
}
