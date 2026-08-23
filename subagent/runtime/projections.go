package runtime

import (
	"context"
	"errors"

	sessionprojection "github.com/gorenx/goren/session/projection"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
)

func (owner *Plugin) registerProjections(
	registry sessionprojection.Registry,
) error {
	if registry == nil {
		return nil
	}
	units := []sessionprojection.Unit{
		subagentprojection.TimingUnit{},
		subagentprojection.IdentityUnit{},
	}
	for _, unit := range units {
		handle, err := registry.Register(unit)
		if err != nil {
			return errors.Join(
				err,
				owner.releaseProjections(context.Background()),
			)
		}
		owner.projections = append(owner.projections, handle)
	}
	return nil
}

func (owner *Plugin) releaseProjections(closeContext context.Context) error {
	var closeErr error
	for index := len(owner.projections) - 1; index >= 0; index-- {
		closeErr = errors.Join(
			closeErr,
			owner.projections[index].Release(closeContext),
		)
	}
	owner.projections = nil
	return closeErr
}
