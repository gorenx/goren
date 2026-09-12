// Package toolruntime owns inherited tool configuration and effective work membership.
package toolruntime

import (
	"bytes"
	"context"
	"errors"
	"slices"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/tools/execution"
	toolregistry "github.com/gorenx/goren/tools/registry"
)

// Snapshot is the effective membership and policy after work has successfully switched.
type Snapshot struct {
	Names   []string
	Schemas []llm.ToolSchema
	// Configuration is the complete Registry input used by the last successful
	// switch, including disabled tools absent from executable membership.
	Configuration toolregistry.Configuration
	// ProviderIDs records the implementation bindings still used by applied tools.
	ProviderIDs map[string]tools.ProviderID
	// Policies maps effective tool names to merged execution collaborators for this snapshot.
	Policies map[string]execution.Policies
}

// Toolset owns only applied work and an unfinished replacement. Registry remains
// authoritative for source and Session configuration, which is read at each boundary.
type Toolset struct {
	agentID session.SessionID
	current []Tool
	pending *Update
	ready   bool
	closing bool
	// versions maps a canonical tool name (e.g. "read_file") to its last applied
	// schema, including Version. It retains history across disabling/removal;
	// membership and current visibility belong to current, not this map.
	versions map[string]llm.ToolSchema
	// configuration is the last fully applied input, including disabled tools.
	// The pending operation owns its target until resource work succeeds.
	configuration toolregistry.Configuration
}

// NewToolset reserves an unconfigured business owner without starting work.
func NewToolset(identifier session.SessionID) (*Toolset, error) {
	if identifier == "" {
		return nil, errors.New("tools: Agent identity is required")
	}
	return &Toolset{agentID: identifier}, nil
}

// Initialize applies inherited Registry input before exposing a tool snapshot.
func (owner *Toolset) Initialize(ctx context.Context, input toolregistry.Configuration, instances Instances) error {
	if owner.ready || owner.pending != nil || owner.closing {
		return errors.New("tools: initialization was already attempted")
	}
	complete, err := owner.Apply(ctx, input, instances)
	if !complete && err == nil {
		return errors.New("tools: initialization is incomplete")
	}
	return err
}

// Apply finishes any pending replacement before applying the supplied configuration.
// Callers must hold the Agent execution boundary until complete is true.
func (owner *Toolset) Apply(ctx context.Context, input toolregistry.Configuration, instances Instances) (bool, error) {
	if ctx == nil || instances == nil {
		return false, errors.New("tools: update context and instances are required")
	}
	if owner.closing {
		return false, errors.New("tools: runtime is closing")
	}
	if owner.pending != nil {
		complete, err := owner.applyUpdate(ctx, instances)
		if !complete || err != nil {
			return complete, err
		}
	}
	entries, err := Merge(input)
	if err != nil {
		return false, err
	}
	err = owner.assignVersions(entries)
	if err != nil {
		return false, err
	}
	owner.pending = NewUpdate(input, entries, owner.current)
	return owner.applyUpdate(ctx, instances)
}

// applyUpdate retains the original revision and progress until all work settles.
func (owner *Toolset) applyUpdate(ctx context.Context, instances Instances) (bool, error) {
	complete, err := owner.pending.Apply(ctx, instances)
	if !complete {
		return false, err
	}
	if err == nil {
		owner.current = cloneTools(owner.pending.tools)
		owner.configuration = cloneConfiguration(owner.pending.configuration)
		owner.ready = true
		if owner.versions == nil {
			owner.versions = make(map[string]llm.ToolSchema)
		}
		for _, entry := range owner.current {
			owner.versions[entry.Schema.Name] = entry.Schema
		}
	}
	owner.pending = nil
	return true, err
}

// Snapshot is unavailable while a replacement or close is unfinished.
func (owner *Toolset) Snapshot() (Snapshot, error) {
	definitions, err := owner.Definitions()
	if err != nil {
		return Snapshot{}, err
	}
	captured := Snapshot{
		Names:         definitions.KnownNames,
		Schemas:       definitions.Schemas,
		Configuration: cloneConfiguration(owner.configuration),
		Policies:      make(map[string]execution.Policies),
		ProviderIDs:   make(map[string]tools.ProviderID),
	}
	for _, entry := range owner.current {
		if entry.ProviderID != "" {
			captured.ProviderIDs[entry.Schema.Name] = entry.ProviderID
		}
		policies := entry.Policies
		policies.Guards = slices.Clone(policies.Guards)
		captured.Policies[entry.Schema.Name] = policies
	}
	return captured, nil
}

func cloneConfiguration(input toolregistry.Configuration) toolregistry.Configuration {
	return toolregistry.Configuration{
		Global: input.Global.Clone(),
		Custom: input.Custom.Clone(),
	}
}

// Definitions returns only the effective model-facing view, with hidden names retained.
func (owner *Toolset) Definitions() (tools.Definitions, error) {
	if !owner.ready || owner.pending != nil || owner.closing {
		return tools.Definitions{}, errors.New("tools: effective tool configuration is unavailable")
	}
	captured := tools.Definitions{
		AgentID: owner.agentID,
	}
	for _, entry := range owner.current {
		captured.KnownNames = append(captured.KnownNames, entry.Schema.Name)
		if entry.Visible {
			schema := entry.Schema
			schema.Parameters = slices.Clone(schema.Parameters)
			captured.Schemas = append(captured.Schemas, schema)
		}
	}
	return captured, nil
}

func (owner *Toolset) assignVersions(entries []Tool) error {
	for index := range entries {
		entry := &entries[index].Schema
		previous := owner.versions[entry.Name]
		entry.Version = previous.Version
		if previous.Version == 0 || previous.Description != entry.Description || !bytes.Equal(previous.Parameters, entry.Parameters) {
			if previous.Version == ^uint64(0) {
				return errors.New("tools: schema version exhausted")
			}
			entry.Version++
		}
	}
	return nil
}
