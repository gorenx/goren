package toolruntime

import (
	"context"
	"errors"
)

// Apply advances the update until settled or an operation requires retry.
// complete=true with an error means rollback is finished; complete=false always
// retains the update barrier. Run outside Actor Receive, using bounded ports.
func (owner *Update) Apply(
	ctx context.Context,
	directory Catalog,
	instances Instances,
) (complete bool, err error) {
	if ctx == nil || directory == nil || instances == nil {
		return false, errors.New("tools: update context and ports are required")
	}
	cleanup := context.WithoutCancel(ctx)
	identifier := owner.configuration.Update.ID
	entries := owner.configuration.Tools
	for {
		switch owner.phase {
		case preparing:
			if ctx.Err() != nil {
				owner.failure = context.Cause(ctx)
				owner.transition(abortingCandidates)
				continue
			}
			if owner.next == len(entries) {
				owner.transition(publishing)
				continue
			}
			owner.attempted = owner.next + 1
			if err := instances.Prepare(ctx, owner.revision, entries[owner.next]); err != nil {
				owner.failure = err
				owner.transition(abortingCandidates)
				continue
			}
			owner.next++
		case publishing:
			if ctx.Err() != nil {
				owner.failure = context.Cause(ctx)
				owner.transition(abortingCandidates)
				continue
			}
			fact, err := directory.Publish(ctx, identifier)
			if err != nil {
				owner.failure = err
				owner.transition(checkingPublication)
				continue
			}
			if err := owner.confirmPublication(fact); err != nil {
				owner.failure = err
				owner.transition(checkingPublication)
				return false, err
			}
		case checkingPublication:
			publication, found, err := directory.Lookup(cleanup, identifier)
			if err != nil {
				return false, errors.Join(owner.failure, err)
			}
			if !found || publication.AgentID != owner.configuration.Update.AgentID {
				return false, errors.Join(owner.failure, errors.New("tools: publication status is unknown"))
			}
			if !publication.Published {
				owner.transition(abortingCandidates)
				continue
			}
			if err := owner.confirmPublication(publication.Catalog); err != nil {
				return false, err
			}
		case committing:
			if owner.next == len(entries) {
				owner.transition(retiring)
				continue
			}
			if err := instances.Commit(cleanup, owner.revision, entries[owner.next].Name); err != nil {
				return false, err
			}
			owner.next++
		case retiring:
			if owner.next == len(entries) {
				owner.transition(removing)
				continue
			}
			if err := instances.Retire(cleanup, owner.revision, entries[owner.next].Name); err != nil {
				return false, err
			}
			owner.next++
		case removing:
			names := owner.removed
			if owner.failure != nil {
				names = owner.added
			}
			if owner.next < len(names) {
				if err := instances.Remove(cleanup, names[owner.next]); err != nil {
					return false, errors.Join(owner.failure, err)
				}
				owner.next++
				continue
			}
			// The cursor stays at the end on failure, so a retry only repeats
			// directory settlement and never removes an instance twice.
			var err error
			if owner.failure != nil {
				err = directory.Abort(cleanup, identifier)
			} else {
				err = directory.Settle(cleanup, identifier)
			}
			if err != nil {
				return false, errors.Join(owner.failure, err)
			}
			owner.transition(finished)
		case abortingCandidates:
			if owner.next == owner.attempted {
				owner.transition(removing)
				continue
			}
			if err := instances.Abort(cleanup, owner.revision, entries[owner.next].Name); err != nil {
				return false, errors.Join(owner.failure, err)
			}
			owner.next++
		case finished:
			return true, owner.failure
		}
	}
}
