package toolruntime

import (
	"context"
	"errors"
	"slices"

	"github.com/gorenx/goren/session"
	toolregistry "github.com/gorenx/goren/tools/registry"
)

// Directory supplies exact configurations as well as update publication facts.
type Directory interface {
	Catalog
	Configure(
		context.Context,
		toolregistry.UpdateID,
		session.SessionID,
		[]toolregistry.Tool,
	) (toolregistry.Configuration, error)
	ReadConfiguration(context.Context, toolregistry.UpdateID) (toolregistry.Configuration, error)
}

// Snapshot is the settled tool membership available for execution.
type Snapshot struct {
	Names []string
}

// Runtime owns one Agent's settled membership and unfinished update. Its caller
// serializes access and drains executions before Apply; it owns no tool resources.
type Runtime struct {
	agentID session.SessionID
	// current is absent until this runtime has completed its own first update.
	// A retained Registry catalog does not prove ownership of live tool instances.
	// registration records a potentially accepted initial configuration until
	// an Update takes over its cleanup responsibility. Errors do not clear it.
	registration toolregistry.UpdateID
	current      *Snapshot
	pending      *Update
	// closing permanently rejects updates and snapshot reads once shutdown begins.
	// Remaining membership and pending work describe cleanup progress without a second cursor.
	closing bool
	// lastID and lastError retain only the latest completed request's result, so
	// a lost completion reply does not require reading an already settled batch.
	lastID    toolregistry.UpdateID
	lastError error
}

// NewRuntime creates an unconfigured Agent tool owner.
func NewRuntime(agentID session.SessionID) (*Runtime, error) {
	if agentID == "" {
		return nil, errors.New("tools: runtime Agent identity is required")
	}
	return &Runtime{
		agentID: agentID,
	}, nil
}

// Initialize registers this Agent's overrides and completes its first tool update.
// After failure, Close must discharge registration and instance responsibilities.
func (owner *Runtime) Initialize(
	ctx context.Context,
	identifier toolregistry.UpdateID,
	overrides []toolregistry.Tool,
	directory Directory,
	instances Instances,
) (err error) {
	if ctx == nil || identifier == "" || directory == nil || instances == nil {
		return errors.New("tools: initialization identity, context and ports are required")
	}
	if owner.closing ||
		owner.current != nil ||
		owner.pending != nil ||
		owner.registration != "" ||
		owner.lastID != "" {
		return errors.New("tools: runtime initialization was already attempted")
	}
	owner.registration = identifier
	defer func() {
		if err != nil {
			owner.closing = true
		}
	}()
	configured, err := directory.Configure(ctx, identifier, owner.agentID, overrides)
	if err != nil {
		return err
	}
	if configured.Update.ID != identifier || configured.Update.AgentID != owner.agentID {
		return errors.New("tools: initial configuration does not match this Agent and update")
	}
	pending, err := NewUpdate(configured, nil)
	if err != nil {
		return err
	}
	owner.pending = pending
	owner.registration = ""
	complete, err := owner.applyUpdate(ctx, directory, instances)
	if !complete && err == nil {
		return errors.New("tools: initialization did not settle its update")
	}
	return err
}

// Apply resumes the exact update until it finishes or needs another attempt.
// A false result retains the execution barrier, including publication uncertainty.
func (owner *Runtime) Apply(ctx context.Context, identifier toolregistry.UpdateID, directory Directory, instances Instances) (bool, error) {
	if ctx == nil || identifier == "" || directory == nil || instances == nil {
		return false, errors.New("tools: update identity, context and ports are required")
	}
	if owner.closing || owner.registration != "" {
		return false, errors.New("tools: runtime is closing or initialization cleanup is pending")
	}
	if owner.pending != nil && owner.pending.configuration.Update.ID != identifier {
		return false, errors.New("tools: another update has unfinished work")
	}
	if owner.pending == nil && owner.lastID == identifier {
		return true, owner.lastError
	}
	if owner.pending == nil {
		configured, err := directory.ReadConfiguration(ctx, identifier)
		if err != nil {
			return false, err
		}
		if configured.Update.ID != identifier ||
			configured.Update.AgentID != owner.agentID {
			return false, errors.New("tools: configuration does not match this Agent and update")
		}
		var names []string
		if owner.current != nil {
			names = owner.current.Names
		}
		pending, err := NewUpdate(configured, names)
		if err != nil {
			return false, err
		}
		owner.pending = pending
	}
	return owner.applyUpdate(ctx, directory, instances)
}

func (owner *Runtime) applyUpdate(ctx context.Context, directory Catalog, instances Instances) (bool, error) {
	complete, err := owner.pending.Apply(ctx, directory, instances)
	if !complete {
		return false, err
	}
	if err == nil {
		owner.current = &Snapshot{
			Names: slices.Clone(owner.pending.configuration.Update.Names),
		}
	}
	owner.lastID = owner.pending.configuration.Update.ID
	owner.lastError = err
	owner.pending = nil
	return true, err
}

// Snapshot rejects reads before initial configuration or during an unfinished
// update; a published directory does not yet prove all instances are usable.
func (owner *Runtime) Snapshot() (Snapshot, error) {
	if owner.closing || owner.pending != nil || owner.current == nil {
		return Snapshot{}, errors.New("tools: runtime has no settled tool snapshot")
	}
	return Snapshot{
		Names: slices.Clone(owner.current.Names),
	}, nil
}
