package catalog

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
) ([]subagent.ListEntry, error) {
	aligned, err := resolveAligned(ctx, candidates, prepared)
	if err != nil {
		return nil, err
	}
	rows := make([]subagent.ListEntry, 0, len(aligned))
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
) ([]subagent.ListEntry, error) {
	rows := make([]subagent.ListEntry, len(candidates))
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

func resolveLive(candidate sessionRecord, prepared listing) subagent.ListEntry {
	projectionSnapshot, err := prepared.dependencies.projections.Snapshot(candidate.live)
	if err != nil {
		return diagnostic(candidate.header.ID, subagent.DiagnosticCorrupt)
	}
	identity, found, err := identityFrom(projectionSnapshot.Values)
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
) subagent.ListEntry {
	if requestContext.Err() != nil {
		return nil
	}
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
	identity, found, err := identityFrom(restored.Snapshot.Values)
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

func identityFrom(
	values sessionprojection.Values,
) (subagentprojection.Identity, bool, error) {
	rawValue, found := values[subagentprojection.IdentityKey]
	if !found {
		return subagentprojection.Identity{}, false, nil
	}
	return subagentprojection.DecodeIdentity(rawValue)
}

func childEntry(
	identifier session.SessionID,
	identity subagentprojection.Identity,
	activity subagent.Activity,
	children bool,
) subagent.ListEntry {
	if identity.Mode == subagent.ModeOneShot {
		return subagent.OneShotChildEntry{
			ID:          identifier,
			Label:       cloneString(identity.Label),
			Activity:    activity,
			HasChildren: children,
		}
	}
	return subagent.ContinuableChildEntry{
		ID:          identifier,
		Label:       *identity.Label,
		Activity:    activity,
		HasChildren: children,
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
		sameString(left.CWD, right.CWD) &&
		sameSessionID(left.ParentSession, right.ParentSession) &&
		sameInt64(left.SeedLength, right.SeedLength) &&
		sameInt64(left.DelegationDepth, right.DelegationDepth)
}

func sameString(left *string, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameSessionID(left *session.SessionID, right *session.SessionID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameInt64(left *int64, right *int64) bool {
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
