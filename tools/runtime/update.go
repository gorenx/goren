// Package toolruntime owns per-Agent tool update business workflows.
package toolruntime

import (
	"context"
	"errors"
	"slices"

	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	"github.com/gorenx/goren/tools/tool"
)

// Catalog is the update workflow's directory port. Lookup must distinguish a
// known unpublished batch from an unknown update; transport errors prove neither.
type Catalog interface {
	Publish(context.Context, toolregistry.UpdateID) (tools.Definitions, error)
	Lookup(context.Context, toolregistry.UpdateID) (toolregistry.Publication, bool, error)
	Abort(context.Context, toolregistry.UpdateID) error
	Settle(context.Context, toolregistry.UpdateID) error
}

// Instances controls existing tool identities through the lifecycle owner.
// Prepare includes candidate registration; Remove confirms resource closure and
// instance removal. Operations must support retry after an uncertain reply.
type Instances interface {
	Prepare(context.Context, tool.Revision, toolregistry.Tool) error
	Commit(context.Context, tool.Revision, string) error
	Retire(context.Context, tool.Revision, string) error
	Abort(context.Context, tool.Revision, string) error
	Remove(context.Context, string) error
}

type updatePhase uint8

const (
	// preparing constructs and registers candidates without changing the active
	// catalog. Failure or cancellation enters candidate rollback.
	preparing updatePhase = iota
	// publishing is reached only after every candidate is registered. No tool
	// implementation switches until the catalog's commit is confirmed.
	publishing
	// checkingPublication retains the barrier after an uncertain publish reply.
	// Query failure stays here; confirmed publication permits switching, while
	// confirmed non-publication permits candidate rollback.
	checkingPublication
	// committing switches each prepared implementation to the published version.
	// Failure retries the unconfirmed tool; publication can no longer be rolled back.
	committing
	// retiring releases replaced implementations after every tool has switched.
	// Failed resource cleanup stays at the same tool and keeps the barrier closed.
	retiring
	// removing closes deleted instances on success, or new instances on rollback.
	// Once removals finish, it settles or aborts the catalog batch; failure of that
	// final operation does not repeat already confirmed instance removals.
	removing
	// abortingCandidates releases every attempted candidate, including one whose
	// preparation failed. Existing implementations remain intact; cleanup errors
	// must be resolved before removing additions and aborting the catalog batch.
	abortingCandidates
	// finished means all update obligations are discharged. A retained failure
	// reports completed rollback; nil failure reports a successfully applied update.
	finished
)

// Update retains only one transaction's business progress. Its caller drains
// tool calls and serializes Apply; this object has no framework or owner references.
type Update struct {
	configuration toolregistry.Configuration
	revision      tool.Revision
	removed       []string
	added         []string
	phase         updatePhase
	next          int
	// attempted includes a failing preparation because it may retain cleanup work.
	attempted int
	// failure survives rollback retries until every cleanup obligation is discharged.
	// A non-nil value in removing identifies rollback; publication clears it.
	failure error
}

// NewUpdate captures one registered configuration and the currently owned names.
// The configuration identity remains fixed through publication and cleanup.
func NewUpdate(input toolregistry.Configuration, previous []string) (*Update, error) {
	if input.Update.ID == "" || input.Update.AgentID == "" ||
		len(input.Tools) != len(input.Update.Names) {
		return nil, errors.New("tools: invalid update configuration")
	}
	names := make(map[string]bool, len(input.Tools))
	for index, entry := range input.Tools {
		if entry.Name == "" || entry.Name != input.Update.Names[index] ||
			names[entry.Name] || entry.Provider == nil {
			return nil, errors.New("tools: inconsistent configured tool membership")
		}
		names[entry.Name] = true
	}
	old := make(map[string]bool, len(previous))
	for _, name := range previous {
		if name == "" || old[name] {
			return nil, errors.New("tools: invalid previous tool membership")
		}
		old[name] = true
	}
	input.Tools = slices.Clone(input.Tools)
	input.Update.Names = slices.Clone(input.Update.Names)
	owner := &Update{
		configuration: input,
		revision:      tool.Revision(string(input.Update.ID)),
	}
	for _, name := range previous {
		if !names[name] {
			owner.removed = append(owner.removed, name)
		}
	}
	for _, entry := range input.Tools {
		if !old[entry.Name] {
			owner.added = append(owner.added, entry.Name)
		}
	}
	return owner, nil
}

func (owner *Update) transition(target updatePhase) {
	owner.phase = target
	owner.next = 0
}

func (owner *Update) confirmPublication(fact tools.Definitions) error {
	if fact.AgentID != owner.configuration.Update.AgentID ||
		!slices.Equal(fact.KnownNames, owner.configuration.Update.Names) {
		return errors.New("tools: publication does not match the requested update")
	}
	owner.failure = nil
	owner.transition(committing)
	return nil
}
