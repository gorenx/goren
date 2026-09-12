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
	"github.com/gorenx/goren/mcpclient"
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
	// InheritGlobal merges global schemas, flags and policies into this Session.
	// False selects only this catalog; an explicitly configured empty catalog
	// has no tools. Unconfigured Sessions use global tools. Global sources must
	// leave this false because they do not inherit other sources.
	InheritGlobal bool
	Schemas       []llm.ToolSchema
	// Required names tools installed by the owning capability that user selection must retain.
	Required []string
	// MCP contains connection definitions. Each Runtime opens its own connections;
	// Registry never stores live transports or discovered Provider identities here.
	MCP []mcpclient.Config
	// ProviderIDs identifies replaceable implementations by canonical tool name.
	// Global sources and independent Sessions may bind their own schemas.
	ProviderIDs map[string]tools.ProviderID
	// Restriction filters the resulting membership. Nil Allow is unrestricted;
	// a non-nil empty Allow admits no tool.
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

// Configuration separates the selected source catalog from Session overrides.
// Global is the Runtime base: shared sources for ordinary Sessions, or the
// explicitly supplied complete catalog for independent Sessions.
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
	// sessions maps a Session ID (e.g. "session-1") to its supplied catalog.
	// Catalogs with InheritGlobal contain overrides; others are complete.
	sessions map[session.SessionID]Catalog
}

// RegisterSource replaces a complete global source. Schema names cannot collide
// with other sources or with additions in Sessions inheriting global tools.
func (owner *Registry) RegisterSource(name string, input Catalog) error {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
		return errors.New("tools: source name is required")
	}
	if input.InheritGlobal {
		return errors.New("tools: global sources cannot inherit global configuration")
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
	for _, selected := range owner.sessions {
		if selected.InheritGlobal {
			if err := rejectSchemaConflicts(captured, selected); err != nil {
				return err
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

// ConfigureSession replaces Session values. InheritGlobal retains global tools
// with distinct schema additions and policy overrides; otherwise the catalog is
// a complete selection.
func (owner *Registry) ConfigureSession(identifier session.SessionID, input Catalog) error {
	if identifier == "" {
		return errors.New("tools: Session identity is required")
	}
	if input.InheritGlobal && len(input.ProviderIDs) != 0 {
		return errors.New("tools: inherited Session configuration cannot select Providers")
	}
	captured, err := snapshotCatalog(input)
	if err != nil {
		return err
	}
	if captured.InheritGlobal {
		for _, source := range owner.sources {
			if err := rejectSchemaConflicts(source, captured); err != nil {
				return err
			}
		}
	}
	if !captured.InheritGlobal && len(captured.MCP) == 0 {
		for name := range captured.Policies {
			if !slices.ContainsFunc(captured.Schemas, func(entry llm.ToolSchema) bool { return entry.Name == name }) {
				return fmt.Errorf("tools: independent policy %q requires an owned schema", name)
			}
		}
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
	selected, configured := owner.sessions[identifier]
	if configured && !selected.InheritGlobal {
		captured := selected.Clone()
		restriction := captured.Restriction
		captured.Restriction = nil
		return Configuration{
			Global: captured,
			Custom: Catalog{
				Restriction: restriction,
			},
		}
	}
	combined := owner.Catalog()
	if !configured {
		selected.InheritGlobal = true
	}
	return Configuration{
		Global: combined,
		Custom: selected.Clone(),
	}
}

// Catalog returns detached registered schemas, Provider bindings and source
// policies. It excludes user flags and Session configuration, so a consumer can
// apply a previously selected Settings snapshot without reading newer overrides.
func (owner *Registry) Catalog() Catalog {
	combined := Catalog{
		Policies:    make(map[string]Policy),
		ProviderIDs: make(map[string]tools.ProviderID),
	}
	for _, entries := range owner.sources {
		combined.Schemas = append(combined.Schemas, entries.Schemas...)
		combined.Required = append(combined.Required, entries.Required...)
		combined.MCP = append(combined.MCP, entries.Clone().MCP...)
		maps.Copy(combined.ProviderIDs, entries.ProviderIDs)
		for name, policy := range entries.Policies {
			combined.Policies[name] = policy
		}
	}
	sort.Slice(
		combined.Schemas,
		func(left int, right int) bool {
			return combined.Schemas[left].Name < combined.Schemas[right].Name
		},
	)
	return combined.Clone()
}

// Clone detaches schema bytes and optional values from caller mutation.
func (input Catalog) Clone() Catalog {
	input.Required = slices.Clone(input.Required)
	input.MCP = slices.Clone(input.MCP)
	for index := range input.MCP {
		input.MCP[index].Args = slices.Clone(input.MCP[index].Args)
	}
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
	servers := make(map[string]struct{}, len(captured.MCP))
	for index, definition := range captured.MCP {
		configuration, err := definition.Validate()
		if err != nil {
			return Catalog{}, err
		}
		if _, found := servers[configuration.ServerName]; found {
			return Catalog{}, errors.New("tools: duplicate MCP connection name")
		}
		servers[configuration.ServerName] = struct{}{}
		captured.MCP[index] = configuration
	}
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
	required := make(map[string]struct{}, len(captured.Required))
	for _, name := range captured.Required {
		if !names[name] {
			return Catalog{}, errors.New("tools: required tool must have an owned schema")
		}
		if _, exists := required[name]; exists {
			return Catalog{}, errors.New("tools: duplicate required tool")
		}
		required[name] = struct{}{}
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

// rejectSchemaConflicts checks registrations that coexist in one Session.
// Updating flags for an existing name does not register another schema.
func rejectSchemaConflicts(left, right Catalog) error {
	names := make(map[string]struct{}, len(left.Schemas))
	for _, entry := range left.Schemas {
		names[entry.Name] = struct{}{}
	}
	for _, entry := range right.Schemas {
		if _, found := names[entry.Name]; found {
			return fmt.Errorf("tools: duplicate tool schema %q", entry.Name)
		}
	}
	return nil
}

// Select projects a complete tool selection onto registered capability definitions.
// It retains required tools, Provider bindings and execution guards without writing
// the selected flags or MCP connections back into the Registry.
func (scoped Configuration) Select(
	installed Catalog,
	flags map[string]tools.Config,
	connections []mcpclient.Config,
) (Configuration, error) {
	selected := installed.Clone()
	if !scoped.Custom.InheritGlobal {
		selected = Catalog{
			Required:    slices.Clone(scoped.Global.Required),
			Policies:    make(map[string]Policy),
			ProviderIDs: make(map[string]tools.ProviderID),
		}
		names := slices.Clone(selected.Required)
		for name := range flags {
			if slices.Contains(selected.Required, name) {
				return Configuration{}, fmt.Errorf("tools: tool %q is required by its capability", name)
			}
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			source := scoped.Global
			index := slices.IndexFunc(source.Schemas, func(entry llm.ToolSchema) bool { return entry.Name == name })
			if index < 0 {
				source = installed
				index = slices.IndexFunc(source.Schemas, func(entry llm.ToolSchema) bool { return entry.Name == name })
			}
			if index < 0 {
				if len(connections) != 0 {
					continue
				}
				return Configuration{}, fmt.Errorf("tools: selected tool %q is not registered", name)
			}
			selected.Schemas = append(selected.Schemas, source.Schemas[index])
			selected.Policies[name] = source.Policies[name]
			if identifier := source.ProviderIDs[name]; identifier != "" {
				selected.ProviderIDs[name] = identifier
			}
		}
	}
	selected.MCP = connections
	if selected.Policies == nil {
		selected.Policies = make(map[string]Policy)
	}
	for name, flags := range flags {
		if slices.Contains(selected.Required, name) {
			return Configuration{}, fmt.Errorf("tools: tool %q is required by its capability", name)
		}
		enabled, visible := flags.Resolve()
		policy := selected.Policies[name]
		policy.Enabled = &enabled
		policy.Visible = &visible
		selected.Policies[name] = policy
	}
	return Configuration{
		Global: selected.Clone(),
		Custom: scoped.Custom.Clone(),
	}, nil
}
