package bound

import (
	"errors"

	"github.com/gorenx/goren/session"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
)

func (owner *Service) parentView(
	parentSession session.Context,
) (subagentprojection.Bound, error) {
	if owner.dependencies.Projections == nil {
		return subagentprojection.Bound{}, unavailableDependency("projections")
	}
	snapshot, err := owner.dependencies.Projections.Snapshot(parentSession)
	if err != nil {
		return subagentprojection.Bound{}, err
	}
	view, found, err := subagentprojection.ReadBound(snapshot.Values)
	if err != nil {
		return subagentprojection.Bound{}, err
	}
	if !found {
		return subagentprojection.Bound{}, errors.New(
			"subagent: Bound projection is not registered",
		)
	}
	return view, nil
}
