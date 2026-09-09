package toolruntime

import (
	"context"
	"errors"

	"github.com/google/uuid"
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
	tools     []Tool
	revision  tool.Revision
	removed   []string
	added     []string
	phase     updatePhase
	next      int
	attempted int
	failure   error
}

// NewUpdate captures a replacement under a fresh operation identity.
func NewUpdate(entries []Tool, previous []string) *Update {
	pending := &Update{
		tools:    cloneTools(entries),
		revision: tool.Revision(uuid.NewString()),
	}
	// names maps each requested tool name to true; absent keys are not in the replacement.
	names := make(map[string]bool, len(entries))
	// old maps each previously active tool name to true, identifying newly added work.
	old := make(map[string]bool, len(previous))
	for _, name := range previous {
		old[name] = true
	}
	for _, entry := range entries {
		name := entry.Schema.Name
		names[name] = true
		if !old[name] {
			pending.added = append(pending.added, name)
		}
	}
	for _, name := range previous {
		if !names[name] {
			pending.removed = append(pending.removed, name)
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
			if owner.next == len(owner.tools) {
				owner.transition(committing)
				continue
			}
			owner.attempted = owner.next + 1
			err := instances.Prepare(ctx, owner.revision, owner.tools[owner.next])
			if err != nil {
				owner.failure = err
				owner.transition(aborting)
				continue
			}
			owner.next++
		case committing:
			if owner.next == len(owner.tools) {
				owner.transition(retiring)
				continue
			}
			err := instances.Commit(cleanup, owner.revision, owner.tools[owner.next].Schema.Name)
			if err != nil {
				return false, err
			}
			owner.next++
		case retiring:
			if owner.next == len(owner.tools) {
				owner.transition(removing)
				continue
			}
			err := instances.Retire(cleanup, owner.revision, owner.tools[owner.next].Schema.Name)
			if err != nil {
				return false, err
			}
			owner.next++
		case aborting:
			if owner.next == owner.attempted {
				owner.transition(removing)
				continue
			}
			err := instances.Abort(cleanup, owner.revision, owner.tools[owner.next].Schema.Name)
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
