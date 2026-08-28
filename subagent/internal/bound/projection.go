package bound

import (
	"errors"

	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
)

func readBoundProjection(
	projections sessionprojection.Registry,
	parentSession session.Context,
) (subagentprojection.Bound, error) {
	if projections == nil {
		return subagentprojection.Bound{}, unavailableDependency("projections")
	}
	snapshot, err := projections.Snapshot(parentSession)
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
