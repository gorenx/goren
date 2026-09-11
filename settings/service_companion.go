package settings

import (
	"context"
	"github.com/gorenx/goren/subagent/companion"
)

// UpdateCompanion replaces a named configuration for future instances only.
func (owner *Service) UpdateCompanion(ctx context.Context, name string, expected int64, configuration companion.Config) (Snapshot, error) {
	next := owner.state.User.Clone()
	if next.Companions == nil {
		next.Companions = make(map[string]companion.Config)
	}
	next.Companions[name] = configuration.Clone()
	return owner.update(ctx, Group{
		Section: CompanionSection,
		Name:    name,
	}, expected, next)
}

// ResetCompanion restores the local group while retaining the last edit revision.
func (owner *Service) ResetCompanion(ctx context.Context, name string, expected int64) (Snapshot, error) {
	next := owner.state.User.Clone()
	delete(next.Companions, name)
	return owner.reset(ctx, Group{
		Section: CompanionSection,
		Name:    name,
	}, expected, next)
}
