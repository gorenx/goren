package toolregistry

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

// UpdateID identifies a unique configuration update until its settlement.
type UpdateID string

// Update declares the complete ordered membership of a candidate catalog.
type Update struct {
	ID      UpdateID
	AgentID session.SessionID
	Names   []string
}

// Candidate is a model-facing tool definition, without runtime resources.
type Candidate struct {
	Schema  llm.ToolSchema
	Visible bool
}

// Registry owns committed Agent catalogs and unsettled publication batches.
// Its caller serializes all access; it performs no IO and has no framework dependencies.
type Registry struct {
	defaults    []Tool
	initialized bool
	catalogs    map[session.SessionID]tools.Definitions
	batches     map[UpdateID]*batch
	// schemas[sessionID][toolName] retains the last committed definition,
	// e.g. schemas["session-1"]["read_file"]. The outer key is also the Agent ID,
	// and the inner key is the canonical tool name. It is retained version history,
	// not the currently executable tool set. Removed tools remain here so
	// re-adding a name cannot reuse an old version.
	schemas map[session.SessionID]map[string]llm.ToolSchema
}

type batch struct {
	configuration *Configuration
	update        Update
	candidates    map[string]Candidate
	published     *tools.Definitions
}

// Begin starts a batch without changing the effective catalog.
func (owner *Registry) Begin(input Update) error {
	if input.ID == "" || input.AgentID == "" {
		return errors.New("tools: update and Agent IDs are required")
	}
	if previous, exists := owner.batches[input.ID]; exists {
		if previous.update.AgentID == input.AgentID &&
			slices.Equal(previous.update.Names, input.Names) {
			return nil
		}
		return errors.New("tools: update ID already has different input")
	}
	for _, pending := range owner.batches {
		if pending.update.AgentID == input.AgentID {
			return errors.New("tools: Agent has an unsettled tool update")
		}
	}
	names := make(map[string]struct{}, len(input.Names))
	for _, name := range input.Names {
		if strings.TrimSpace(name) == "" ||
			strings.TrimSpace(name) != name ||
			name == tools.RunCodeName {
			return fmt.Errorf("tools: invalid candidate name %q", name)
		}
		if _, exists := names[name]; exists {
			return fmt.Errorf("tools: duplicate candidate %q", name)
		}
		names[name] = struct{}{}
	}
	input.Names = slices.Clone(input.Names)
	if owner.batches == nil {
		owner.batches = make(map[UpdateID]*batch)
	}
	owner.batches[input.ID] = &batch{
		update:     input,
		candidates: make(map[string]Candidate, len(input.Names)),
	}
	return nil
}

// Register validates and captures one candidate. Conflicting retries are rejected.
func (owner *Registry) Register(identifier UpdateID, input Candidate) error {
	pending, exists := owner.batches[identifier]
	if !exists {
		return errors.New("tools: unknown update")
	}
	position := slices.Index(pending.update.Names, input.Schema.Name)
	if position < 0 {
		return errors.New("tools: candidate is not in the update")
	}
	if pending.configuration != nil &&
		pending.configuration.Tools[position].Visible != input.Visible {
		return errors.New("tools: candidate visibility differs from registered configuration")
	}
	if input.Schema.Version != 0 {
		return errors.New("tools: candidate schema version is assigned by Registry")
	}
	parameters, err := tools.SnapshotObjectSchema(input.Schema.Parameters)
	if err != nil {
		return err
	}
	input.Schema.Parameters = parameters
	if previous, exists := pending.candidates[input.Schema.Name]; exists {
		if previous.Visible == input.Visible &&
			previous.Schema.Name == input.Schema.Name &&
			previous.Schema.Description == input.Schema.Description &&
			bytes.Equal(previous.Schema.Parameters, input.Schema.Parameters) {
			return nil
		}
		return errors.New("tools: candidate already registered with different definition")
	}
	if pending.published != nil {
		return errors.New("tools: update already published")
	}
	pending.candidates[input.Schema.Name] = input
	return nil
}

// Publish atomically installs a complete catalog or returns the prior publication.
func (owner *Registry) Publish(identifier UpdateID) (tools.Definitions, error) {
	pending, exists := owner.batches[identifier]
	if !exists {
		return tools.Definitions{}, errors.New("tools: unknown update")
	}
	if pending.published != nil {
		return clonePublication(*pending.published), nil
	}
	if len(pending.candidates) != len(pending.update.Names) {
		return tools.Definitions{}, errors.New("tools: candidate catalog is incomplete")
	}
	committed := tools.Definitions{
		AgentID:    pending.update.AgentID,
		KnownNames: slices.Clone(pending.update.Names),
	}
	previous := owner.catalogs[pending.update.AgentID]
	assigned := make(map[string]llm.ToolSchema, len(pending.candidates))
	for _, name := range pending.update.Names {
		entry := pending.candidates[name]
		prior := owner.schemas[pending.update.AgentID][name]
		entry.Schema.Version = prior.Version
		unchanged := slices.Contains(previous.KnownNames, name) &&
			prior.Description == entry.Schema.Description &&
			bytes.Equal(prior.Parameters, entry.Schema.Parameters)
		if !unchanged {
			if prior.Version == ^uint64(0) {
				return tools.Definitions{}, fmt.Errorf("tools: schema version exhausted for %q", name)
			}
			entry.Schema.Version++
		}
		assigned[name] = entry.Schema
		if entry.Visible {
			committed.Schemas = append(committed.Schemas, entry.Schema)
		}
	}
	if owner.catalogs == nil {
		owner.catalogs = make(map[session.SessionID]tools.Definitions)
	}
	if owner.schemas == nil {
		owner.schemas = make(map[session.SessionID]map[string]llm.ToolSchema)
	}
	if owner.schemas[committed.AgentID] == nil {
		owner.schemas[committed.AgentID] = make(map[string]llm.ToolSchema)
	}
	for name, entry := range assigned {
		owner.schemas[committed.AgentID][name] = entry
	}
	owner.catalogs[committed.AgentID] = committed
	pending.published = &committed
	return clonePublication(committed), nil
}

// Read returns a detached committed baseline; candidates are never exposed.
func (owner *Registry) Read(identifier session.SessionID) (tools.Definitions, bool) {
	committed, exists := owner.catalogs[identifier]
	return clonePublication(committed), exists
}

// Lookup distinguishes unknown, preparing, and published updates without retrying effects.
func (owner *Registry) Lookup(identifier UpdateID) (Publication, bool) {
	pending, exists := owner.batches[identifier]
	if !exists {
		return Publication{}, false
	}
	outcome := Publication{
		AgentID: pending.update.AgentID,
	}
	if pending.published != nil {
		outcome.Published = true
		outcome.Catalog = clonePublication(*pending.published)
	}
	return outcome, true
}

// Publication reports whether an accepted update has reached its commit point.
type Publication struct {
	AgentID   session.SessionID
	Published bool
	Catalog   tools.Definitions
}

// Abort forgets an unpublished batch after the caller has released its candidates.
func (owner *Registry) Abort(identifier UpdateID) error {
	pending, exists := owner.batches[identifier]
	if !exists {
		return nil
	}
	if pending.published != nil {
		return errors.New("tools: cannot abort a published update")
	}
	delete(owner.batches, identifier)
	return nil
}

// Settle discards the lookup record after runtime switching and resource release.
// The caller must never reuse an update ID after settlement.
func (owner *Registry) Settle(identifier UpdateID) error {
	pending, exists := owner.batches[identifier]
	if !exists {
		return nil
	}
	if pending.published == nil {
		return errors.New("tools: update has not been published")
	}
	delete(owner.batches, identifier)
	return nil
}

func clonePublication(input tools.Definitions) tools.Definitions {
	input.KnownNames = slices.Clone(input.KnownNames)
	input.Schemas = slices.Clone(input.Schemas)
	for index := range input.Schemas {
		input.Schemas[index].Parameters = slices.Clone(input.Schemas[index].Parameters)
	}
	return input
}
