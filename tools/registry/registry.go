// Package toolregistry owns tool schemas and policies from global sources and Sessions.
package toolregistry

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

// Policy contains optional configuration; omission inherits the global value.
// Runtime applies defaults only after merging all applicable configuration.
type Policy struct {
	Enabled *bool
	Visible *bool
	// Execution policy collaborators are borrowed immutable capabilities.
	Before    tools.PrePolicy
	After     tools.PostPolicy
	Execution tools.ExecuteMiddleware
	Results   tools.ResultObserver
	Guards    []tools.ToolGuard
}

// Catalog is one source's or Session's unmerged tool configuration. Policy-only
// entries override inherited tools without copying their schemas.
type Catalog struct {
	Schemas []llm.ToolSchema
	// ProviderIDs identifies replaceable implementations by canonical tool name.
	// Only global sources may supply these values, for their own schemas.
	ProviderIDs map[string]tools.ProviderID
	// Restriction intersects inherited membership. Nil Allow is unrestricted;
	// a non-nil empty Allow admits no inherited tool.
	Restriction *ToolRestriction
	// Policies maps a canonical tool name to explicitly configured policy fields.
	// For example, a false value at *Policies["read_file"].Visible hides that tool after merging.
	// A Session may override policy without copying the inherited schema.
	Policies map[string]Policy
}

type ToolRestriction struct {
	Allow []string
	Deny  []string
}

func (restriction ToolRestriction) Clone() ToolRestriction {
	return ToolRestriction{
		Allow: slices.Clone(restriction.Allow),
		Deny:  slices.Clone(restriction.Deny),
	}
}

// Configuration preserves global and Session values separately for Runtime merging.
type Configuration struct {
	Global Catalog
	Custom Catalog
}

// Registry stores configuration only. Its caller serializes access; no method
// constructs a tool, merges Session overrides, or tracks resource publication.
type Registry struct {
	// sources maps a global source ID (e.g. "local" or "mcp:docs") to that
	// source's complete schema and policy set. Values never contain workers.
	sources map[string]Catalog
	// sessions maps a Session ID (e.g. "session-1") to only its custom schemas
	// and policy fields. Global values are not copied into these entries.
	sessions map[session.SessionID]Catalog
	// configured contains complete user flags accepted independently of source
	// publication. It never owns Schema or execution-policy capabilities.
	configured map[string]tools.Config
}

// ConfigureTool accepts a complete configuration for an existing tool. Schema,
// Provider identity and execution-policy capabilities remain with their source.
func (owner *Registry) ConfigureTool(name string, configuration tools.Config) error {
	if !validName(name) {
		return errors.New("tools: invalid configuration name")
	}
	found := false
	for _, source := range owner.sources {
		for _, entry := range source.Schemas {
			if entry.Name == name {
				found = true
			}
		}
	}
	if !found {
		return errors.New("tools: configuration requires an existing tool")
	}
	if owner.configured == nil {
		owner.configured = make(map[string]tools.Config)
	}
	owner.configured[name] = configuration.Clone()
	return nil
}

// RegisterSource replaces a complete global source. Names cannot collide across sources.
func (owner *Registry) RegisterSource(name string, input Catalog) error {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
		return errors.New("tools: source name is required")
	}
	captured, err := snapshotCatalog(input)
	if err != nil {
		return err
	}
	for other, entries := range owner.sources {
		if other == name {
			continue
		}
		for _, incoming := range captured.Schemas {
			for _, existing := range entries.Schemas {
				if incoming.Name == existing.Name {
					return fmt.Errorf("tools: schema %q already belongs to source %q", incoming.Name, other)
				}
			}
		}
		for key := range captured.Policies {
			if _, exists := entries.Policies[key]; exists {
				return fmt.Errorf("tools: policy %q already belongs to source %q", key, other)
			}
		}
	}
	err = assignVersions(&captured, owner.sources[name])
	if err != nil {
		return err
	}
	if owner.sources == nil {
		owner.sources = make(map[string]Catalog)
	}
	owner.sources[name] = captured
	return nil
}

// RemoveSource withdraws its schemas and policies together; runtimes decide how to apply it.
func (owner *Registry) RemoveSource(name string) { delete(owner.sources, name) }

// ConfigureSession replaces only custom values; inherited schemas are never copied here.
func (owner *Registry) ConfigureSession(identifier session.SessionID, input Catalog) error {
	if identifier == "" {
		return errors.New("tools: Session identity is required")
	}
	if len(input.ProviderIDs) != 0 {
		return errors.New("tools: Session configuration cannot select Providers")
	}
	captured, err := snapshotCatalog(input)
	if err != nil {
		return err
	}
	err = assignVersions(&captured, owner.sessions[identifier])
	if err != nil {
		return err
	}
	if owner.sessions == nil {
		owner.sessions = make(map[session.SessionID]Catalog)
	}
	owner.sessions[identifier] = captured
	return nil
}

// RemoveSession restores inheritance without withdrawing global configuration.
func (owner *Registry) RemoveSession(identifier session.SessionID) {
	delete(owner.sessions, identifier)
}

// Read returns detached unmerged values. Global names have deterministic order.
func (owner *Registry) Read(identifier session.SessionID) Configuration {
	combined := Catalog{
		Policies:    make(map[string]Policy),
		ProviderIDs: make(map[string]tools.ProviderID),
	}
	for _, entries := range owner.sources {
		combined.Schemas = append(combined.Schemas, entries.Schemas...)
		maps.Copy(combined.ProviderIDs, entries.ProviderIDs)
		for name, policy := range entries.Policies {
			combined.Policies[name] = policy
		}
	}
	for _, entry := range combined.Schemas {
		if configuration, present := owner.configured[entry.Name]; present {
			enabled, visible := configuration.Resolve()
			policy := combined.Policies[entry.Name]
			policy.Enabled = &enabled
			policy.Visible = &visible
			combined.Policies[entry.Name] = policy
		}
	}
	sort.Slice(combined.Schemas, func(left int, right int) bool { return combined.Schemas[left].Name < combined.Schemas[right].Name })
	return Configuration{
		Global: combined.Clone(),
		Custom: owner.sessions[identifier].Clone(),
	}
}

// Clone detaches schema bytes and optional values from caller mutation.
func (input Catalog) Clone() Catalog {
	input.Schemas = slices.Clone(input.Schemas)
	input.ProviderIDs = maps.Clone(input.ProviderIDs)
	if input.Restriction != nil {
		input.Restriction = &ToolRestriction{
			Allow: slices.Clone(input.Restriction.Allow),
			Deny:  slices.Clone(input.Restriction.Deny),
		}
	}
	for index := range input.Schemas {
		input.Schemas[index].Parameters = slices.Clone(input.Schemas[index].Parameters)
	}
	if input.Policies != nil {
		// detached preserves tool-name keys with policy values whose mutable fields
		// are copied below; execution collaborators remain borrowed.
		detached := make(map[string]Policy, len(input.Policies))
		for name, entry := range input.Policies {
			if entry.Enabled != nil {
				value := *entry.Enabled
				entry.Enabled = &value
			}
			if entry.Visible != nil {
				value := *entry.Visible
				entry.Visible = &value
			}
			entry.Guards = slices.Clone(entry.Guards)
			detached[name] = entry
		}
		input.Policies = detached
	}
	return input
}

func snapshotCatalog(input Catalog) (Catalog, error) {
	captured := input.Clone()
	// names maps each validated schema name to true to reject duplicate entries.
	names := make(map[string]bool, len(captured.Schemas))
	for index := range captured.Schemas {
		entry := &captured.Schemas[index]
		if !validName(entry.Name) || names[entry.Name] {
			return Catalog{}, fmt.Errorf("tools: invalid or duplicate schema name %q", entry.Name)
		}
		if entry.Version != 0 {
			return Catalog{}, errors.New("tools: Registry assigns schema versions")
		}
		parameters, err := tools.SnapshotObjectSchema(entry.Parameters)
		if err != nil {
			return Catalog{}, err
		}
		entry.Parameters = parameters
		names[entry.Name] = true
	}
	for name := range captured.Policies {
		if !validName(name) {
			return Catalog{}, fmt.Errorf("tools: invalid policy name %q", name)
		}
	}
	for name, identifier := range captured.ProviderIDs {
		if !names[name] ||
			identifier == "" ||
			strings.TrimSpace(string(identifier)) != string(identifier) {
			return Catalog{}, errors.New("tools: Provider identity requires an owned schema and a nonempty value")
		}
	}
	return captured, nil
}

func validName(name string) bool {
	return name != "" && strings.TrimSpace(name) == name && name != tools.RunCodeName
}

func assignVersions(input *Catalog, previous Catalog) error {
	for index := range input.Schemas {
		incoming := &input.Schemas[index]
		incoming.Version = 1
		for _, existing := range previous.Schemas {
			if existing.Name != incoming.Name {
				continue
			}
			incoming.Version = existing.Version
			if incoming.Description != existing.Description || !bytes.Equal(incoming.Parameters, existing.Parameters) {
				if existing.Version == ^uint64(0) {
					return errors.New("tools: schema version exhausted")
				}
				incoming.Version++
			}
			break
		}
	}
	return nil
}
