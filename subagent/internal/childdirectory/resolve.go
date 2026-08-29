package childdirectory

import (
	"context"
	"sync"

	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/subagent"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
)

const coldReadConcurrency = 4

func resolveRows(
	ctx context.Context,
	candidates []sessionRecord,
	prepared listing,
) ([]subagent.ChildEntry, error) {
	aligned, err := resolveAligned(ctx, candidates, prepared)
	if err != nil {
		return nil, err
	}
	rows := make([]subagent.ChildEntry, 0, len(aligned))
	for _, row := range aligned {
		if row != nil {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func resolveAligned(
	ctx context.Context,
	candidates []sessionRecord,
	prepared listing,
) ([]subagent.ChildEntry, error) {
	rows := make([]subagent.ChildEntry, len(candidates))
	type coldRead struct {
		index   int
		session sessionRecord
	}
	cold := make([]coldRead, 0)
	for index, candidate := range candidates {
		if candidate.live == nil {
			cold = append(cold, coldRead{
				index:   index,
				session: candidate,
			})
			continue
		}
		rows[index] = resolveLive(candidate, prepared)
	}
	if prepared.dependencies.persistence == nil || len(cold) == 0 {
		return rows, listingContextError(ctx)
	}
	jobs := make(chan coldRead)
	workerCount := min(coldReadConcurrency, len(cold))
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for job := range jobs {
				rows[job.index] = resolveCold(
					ctx,
					job.session,
					prepared,
				)
			}
		}()
	}
	for _, job := range cold {
		jobs <- job
	}
	close(jobs)
	workers.Wait()
	return rows, listingContextError(ctx)
}

func resolveLive(candidate sessionRecord, prepared listing) subagent.ChildEntry {
	projectionSnapshot, err := prepared.dependencies.projections.Snapshot(candidate.live)
	if err != nil {
		return diagnostic(candidate.header.ID, subagent.DiagnosticCorrupt)
	}
	identity, found, err := subagentprojection.ReadIdentity(
		projectionSnapshot.Values,
	)
	if err != nil {
		return diagnostic(candidate.header.ID, subagent.DiagnosticCorrupt)
	}
	if !found {
		return nil
	}
	return childEntry(
		candidate.header.ID,
		identity,
		subagent.ActivityRunning,
		hasChildren(candidate.header.ID, prepared),
	)
}

func resolveCold(
	requestContext context.Context,
	candidate sessionRecord,
	prepared listing,
) subagent.ChildEntry {
	if requestContext.Err() != nil {
		return nil
	}
	if entry, found := cachedColdEntry(candidate, prepared); found {
		return entry
	}
	return restoredColdEntry(requestContext, candidate, prepared)
}

func cachedColdEntry(
	candidate sessionRecord,
	prepared listing,
) (subagent.ChildEntry, bool) {
	if prepared.dependencies.cache == nil {
		return nil, false
	}
	projectionSnapshot, err := prepared.dependencies.cache.CachedSnapshot(
		candidate.header,
	)
	if err != nil || projectionSnapshot == nil {
		return nil, false
	}
	identity, found, err := subagentprojection.ReadIdentity(
		projectionSnapshot.Values,
	)
	if err != nil || !found || !identityAfterSeed(identity, candidate.header) {
		return nil, false
	}
	return childEntry(
		candidate.header.ID,
		identity,
		subagent.ActivityInactive,
		hasChildren(candidate.header.ID, prepared),
	), true
}

func restoredColdEntry(
	requestContext context.Context,
	candidate sessionRecord,
	prepared listing,
) subagent.ChildEntry {
	inspection, err := prepared.dependencies.persistence.Inspect(
		requestContext,
		candidate.header.ID,
	)
	if err != nil {
		return diagnostic(candidate.header.ID, subagent.DiagnosticUnavailable)
	}
	if !sameLifecycle(inspection.Header, candidate.header) {
		return diagnostic(candidate.header.ID, subagent.DiagnosticCorrupt)
	}
	restored, err := prepared.dependencies.projections.Restore(
		sessionprojection.Checkpoint{},
		inspection.Events,
		0,
	)
	if err != nil {
		return diagnostic(candidate.header.ID, subagent.DiagnosticCorrupt)
	}
	identity, found, err := subagentprojection.ReadIdentity(
		restored.Snapshot.Values,
	)
	if err != nil || !found {
		return diagnostic(candidate.header.ID, subagent.DiagnosticCorrupt)
	}
	return childEntry(
		candidate.header.ID,
		identity,
		subagent.ActivityInactive,
		hasChildren(candidate.header.ID, prepared),
	)
}

func identityAfterSeed(
	identity subagentprojection.Identity,
	metadata session.Header,
) bool {
	seedLength := int64(0)
	if metadata.SeedLength != nil {
		seedLength = *metadata.SeedLength
	}
	return identity.Seq >= seedLength
}

func childEntry(
	identifier session.SessionID,
	identity subagentprojection.Identity,
	activity subagent.Activity,
	children bool,
) subagent.ChildEntry {
	switch identity.Mode {
	case subagent.ModeOneShot:
		return subagent.OneShotChildEntry{
			ID:          identifier,
			Label:       cloneString(identity.Label),
			Activity:    activity,
			HasChildren: children,
		}
	case subagent.ModeContinuable:
		return subagent.ContinuableChildEntry{
			ID:          identifier,
			Label:       *identity.Label,
			Activity:    activity,
			HasChildren: children,
		}
	case subagent.ModeBound:
		return subagent.BoundChildEntry{
			ID:          identifier,
			Label:       *identity.Label,
			Activity:    activity,
			HasChildren: children,
		}
	default:
		return diagnostic(identifier, subagent.DiagnosticCorrupt)
	}
}

func diagnostic(
	identifier session.SessionID,
	reason subagent.DiagnosticReason,
) subagent.DiagnosticEntry {
	return subagent.DiagnosticEntry{
		ID:     identifier,
		Reason: reason,
	}
}

func hasChildren(identifier session.SessionID, prepared listing) bool {
	_, found := prepared.parents[identifier]
	return found
}

func listingContextError(requestContext context.Context) error {
	if requestErr := requestContext.Err(); requestErr != nil {
		return cancelled(requestErr)
	}
	return nil
}

func sameLifecycle(left session.Header, right session.Header) bool {
	return left.Version == right.Version &&
		left.ID == right.ID &&
		left.CreatedAt == right.CreatedAt &&
		sameOptional(left.CWD, right.CWD) &&
		sameOptional(left.ParentSession, right.ParentSession) &&
		sameOptional(left.SeedLength, right.SeedLength) &&
		sameOptional(left.DelegationDepth, right.DelegationDepth)
}

func sameOptional[Value comparable](left *Value, right *Value) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneString(source *string) *string {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}
