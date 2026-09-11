// Package toolruntime owns inherited tool configuration and effective work membership.
package toolruntime

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/tools/execution"
	toolregistry "github.com/gorenx/goren/tools/registry"
)

// Directory provides Registry facts; it owns no execution or publication transaction.
type Directory interface {
	Read(context.Context, session.SessionID) (toolregistry.Configuration, error)
}

// Snapshot is the effective membership and policy after work has successfully switched.
type Snapshot struct {
	Names   []string
	Schemas []llm.ToolSchema
	// Policies maps effective tool names to merged execution collaborators for this snapshot.
	Policies map[string]execution.Policies
}

// Runtime owns only applied work and an unfinished replacement. Registry remains
// authoritative for source and Session configuration, which is read at each boundary.
type Runtime struct {
	agentID session.SessionID
	current []Tool
	pending *Update
	ready   bool
	closing bool
	// versions maps a canonical tool name (e.g. "read_file") to its last applied
	// schema, including Version. It retains history across disabling/removal;
	// membership and current visibility belong to current, not this map.
	versions map[string]llm.ToolSchema
}

// NewRuntime reserves an unconfigured business owner without starting work.
func NewRuntime(identifier session.SessionID) (*Runtime, error) {
	if identifier == "" {
		return nil, errors.New("tools: Agent identity is required")
	}
	return &Runtime{agentID: identifier}, nil
}

// Initialize applies inherited Registry input before exposing a tool snapshot.
func (owner *Runtime) Initialize(ctx context.Context, directory Directory, instances Instances) error {
	if owner.ready || owner.pending != nil || owner.closing {
		return errors.New("tools: initialization was already attempted")
	}
	complete, err := owner.Apply(ctx, directory, instances)
	if !complete && err == nil {
		return errors.New("tools: initialization is incomplete")
	}
	return err
}

// Apply merges current Registry facts or resumes the exact unfinished replacement.
// Callers must hold the Agent execution boundary until complete is true.
func (owner *Runtime) Apply(ctx context.Context, directory Directory, instances Instances) (bool, error) {
	if ctx == nil || directory == nil || instances == nil {
		return false, errors.New("tools: update context and ports are required")
	}
	if owner.closing {
		return false, errors.New("tools: runtime is closing")
	}
	if owner.pending == nil {
		input, err := directory.Read(ctx, owner.agentID)
		if err != nil {
			return false, err
		}
		entries, err := Merge(input)
		if err != nil {
			return false, err
		}
		var previous []string
		for _, entry := range owner.current {
			previous = append(previous, entry.Schema.Name)
		}
		err = owner.assignVersions(entries)
		if err != nil {
			return false, err
		}
		if owner.ready && sameTools(entries, owner.current) {
			return true, nil
		}
		owner.pending = NewUpdate(entries, previous)
	}
	complete, err := owner.pending.Apply(ctx, instances)
	if !complete {
		return false, err
	}
	if err == nil {
		owner.current = cloneTools(owner.pending.tools)
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
func (owner *Runtime) Snapshot() (Snapshot, error) {
	if !owner.ready || owner.pending != nil || owner.closing {
		return Snapshot{}, errors.New("tools: effective tool configuration is unavailable")
	}
	captured := Snapshot{Policies: make(map[string]execution.Policies)}
	for _, entry := range owner.current {
		captured.Names = append(captured.Names, entry.Schema.Name)
		policies := entry.Policies
		policies.Guards = slices.Clone(policies.Guards)
		captured.Policies[entry.Schema.Name] = policies
		if entry.Visible {
			schema := entry.Schema
			schema.Parameters = slices.Clone(schema.Parameters)
			captured.Schemas = append(captured.Schemas, schema)
		}
	}
	return captured, nil
}

// Definitions returns only the effective model-facing view, with hidden names retained.
func (owner *Runtime) Definitions() (tools.Definitions, error) {
	captured, err := owner.Snapshot()
	return tools.Definitions{
		AgentID:    owner.agentID,
		Schemas:    captured.Schemas,
		KnownNames: captured.Names,
	}, err
}

func (owner *Runtime) assignVersions(entries []Tool) error {
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

// sameTools compares borrowed policy identities without traversing their mutable
// internals. Shared policy owners may be serving other Agents concurrently.
func sameTools(left, right []Tool) bool {
	if len(left) != len(right) {
		return false
	}
	for index, entry := range left {
		other := right[index]
		if entry.Schema.Name != other.Schema.Name ||
			entry.ProviderID != other.ProviderID ||
			entry.Schema.Version != other.Schema.Version ||
			entry.Schema.Description != other.Schema.Description ||
			!bytes.Equal(entry.Schema.Parameters, other.Schema.Parameters) ||
			entry.Visible != other.Visible ||
			!samePolicies(entry.Policies, other.Policies) {
			return false
		}
	}
	return true
}

func samePolicies(left, right execution.Policies) bool {
	if !sameCapability(left.Before, right.Before) ||
		!sameCapability(left.After, right.After) ||
		!sameCapability(left.Execution, right.Execution) ||
		!sameCapability(left.Results, right.Results) ||
		len(left.Guards) != len(right.Guards) {
		return false
	}
	for index, guard := range left.Guards {
		if !sameCapability(guard, right.Guards[index]) {
			return false
		}
	}
	return true
}

func sameCapability(left, right any) bool {
	kind := reflect.TypeOf(left)
	if kind != reflect.TypeOf(right) {
		return false
	}
	if kind == nil {
		return true
	}
	// Non-comparable value capabilities cannot prove stable identity; apply them.
	return kind.Comparable() && left == right
}
