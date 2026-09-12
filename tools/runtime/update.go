package toolruntime

import (
	"bytes"
	"context"
	"errors"

	"github.com/google/uuid"
	toolregistry "github.com/gorenx/goren/tools/registry"
	"github.com/gorenx/goren/tools/tool"
)

// Instances performs work on the common ToolActors. Only the lifecycle owner
// creates or destroys Actors; each operation retains its original identity on retry.
type Instances interface {
	Prepare(context.Context, tool.Revision, Tool) error
	Commit(context.Context, tool.Revision, string) error
	Retire(context.Context, tool.Revision, string) error
	Abort(context.Context, tool.Revision, string) error
	Remove(context.Context, string) error
}

type updatePhase uint8

const (
	// preparing opens replacement work while the old work remains available.
	preparing updatePhase = iota
	// committing switches prepared work; cancellation can no longer roll it back.
	committing
	// retiring releases the work replaced by a successful switch.
	retiring
	// removing releases removed tools, or newly created tools after rollback.
	removing
	// aborting releases all attempted preparations, including failed construction.
	aborting
	// finished confirms that every resource obligation is discharged.
	finished
)

// Update owns one replacement and its cleanup obligations, never Registry state.
type Update struct {
	// configuration is the exact source input that produced tools. It remains
	// paired with this operation across retries and is adopted only on success.
	configuration toolregistry.Configuration
	tools         []Tool
	// preparations contains only new or changed implementations; policy changes
	// are adopted with the target collection without reopening Providers.
	preparations []Tool
	revision     tool.Revision
	removed      []string
	added        []string
	phase        updatePhase
	next         int
	attempted    int
	failure      error
}

// NewUpdate captures a replacement under a fresh operation identity.
func NewUpdate(input toolregistry.Configuration, entries []Tool, previous []Tool) *Update {
	pending := &Update{
		configuration: cloneConfiguration(input),
		tools:         cloneTools(entries),
		revision:      tool.Revision(uuid.NewString()),
	}
	names := make(map[string]struct{}, len(entries))
	old := make(map[string]Tool, len(previous))
	for _, entry := range previous {
		old[entry.Schema.Name] = entry
	}
	for _, entry := range pending.tools {
		name := entry.Schema.Name
		names[name] = struct{}{}
		before, found := old[name]
		if !found {
			pending.added = append(pending.added, name)
		}
		if !found || !sameImplementation(entry, before) {
			pending.preparations = append(pending.preparations, entry)
		}
	}
	for _, entry := range previous {
		if _, found := names[entry.Schema.Name]; !found {
			pending.removed = append(pending.removed, entry.Schema.Name)
		}
	}
	return pending
}

// Apply runs outside Receive and retains exact cleanup progress on failure.
// Cancellation rolls back before switching; after switching it drains cleanup.
func (owner *Update) Apply(ctx context.Context, instances Instances) (bool, error) {
	if ctx == nil || instances == nil {
		return false, errors.New("tools: update context and instances are required")
	}
	cleanup := context.WithoutCancel(ctx)
	for {
		switch owner.phase {
		case preparing:
			if ctx.Err() != nil {
				owner.failure = context.Cause(ctx)
				owner.transition(aborting)
				continue
			}
			if owner.next == len(owner.preparations) {
				owner.transition(committing)
				continue
			}
			owner.attempted = owner.next + 1
			err := instances.Prepare(ctx, owner.revision, owner.preparations[owner.next])
			if err != nil {
				owner.failure = err
				owner.transition(aborting)
				continue
			}
			owner.next++
		case committing:
			if owner.next == len(owner.preparations) {
				owner.transition(retiring)
				continue
			}
			err := instances.Commit(cleanup, owner.revision, owner.preparations[owner.next].Schema.Name)
			if err != nil {
				return false, err
			}
			owner.next++
		case retiring:
			if owner.next == len(owner.preparations) {
				owner.transition(removing)
				continue
			}
			err := instances.Retire(cleanup, owner.revision, owner.preparations[owner.next].Schema.Name)
			if err != nil {
				return false, err
			}
			owner.next++
		case aborting:
			if owner.next == owner.attempted {
				owner.transition(removing)
				continue
			}
			err := instances.Abort(cleanup, owner.revision, owner.preparations[owner.next].Schema.Name)
			if err != nil {
				return false, errors.Join(owner.failure, err)
			}
			owner.next++
		case removing:
			names := owner.removed
			if owner.failure != nil {
				names = owner.added
			}
			if owner.next == len(names) {
				owner.transition(finished)
				continue
			}
			err := instances.Remove(cleanup, names[owner.next])
			if err != nil {
				return false, errors.Join(owner.failure, err)
			}
			owner.next++
		case finished:
			return true, owner.failure
		}
	}
}

func (owner *Update) transition(next updatePhase) {
	owner.phase = next
	owner.next = 0
}

// sameImplementation compares only inputs used to provision a Tool. Visibility
// and execution policies belong to the effective collection, not Provider resources.
func sameImplementation(left, right Tool) bool {
	return left.ProviderID == right.ProviderID &&
		left.Schema.Name == right.Schema.Name &&
		left.Schema.Version == right.Schema.Version &&
		left.Schema.Description == right.Schema.Description &&
		bytes.Equal(left.Schema.Parameters, right.Schema.Parameters)
}
