package runtime

import (
	"context"

	"github.com/gorenx/goren/plugin"
)

const subagentActivationOwnerName = "@goren/subagent/activation-owner"

// activationOwner is the structural parent of continuable Agent trees. Its
// child tree drains before the Subagent Plugin's business Services are
// disposed, while one-shot Runs remain owned by their returned handles.
type activationOwner struct {
	plugin.Base
}

func (*activationOwner) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: subagentActivationOwnerName,
	}
}

func (*activationOwner) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (*activationOwner) Dispose(context.Context) error {
	return nil
}

var _ plugin.Plugin = (*activationOwner)(nil)
