package tools

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

// UpdateID identifies a unique configuration update until its settlement.
type UpdateID string

// Update declares the complete ordered membership of a candidate catalog.
type Update struct {
	ID              UpdateID
	AgentID         session.SessionID
	ExpectedVersion uint64
	Names           []string
}

// Candidate is a ToolActor's model-facing definition, without runtime resources.
type Candidate struct {
	Schema  llm.ToolSchema
	Visible bool
}

// Registry owns committed Agent catalogs and unsettled publication batches.
// Its caller serializes all access; it performs no IO and has no framework dependencies.
type Registry struct {
	catalogs map[session.SessionID]DefinitionsChanged
	batches  map[UpdateID]*batch
}

type batch struct {
	update     Update
	candidates map[string]Candidate
	published  *DefinitionsChanged
}

// Begin starts a batch without changing the effective catalog.
func (owner *Registry) Begin(input Update) error {
	if input.ID == "" || input.AgentID == "" {
		return errors.New("tools: update and Agent IDs are required")
	}
	if previous, exists := owner.batches[input.ID]; exists {
		if previous.update.AgentID == input.AgentID &&
			previous.update.ExpectedVersion == input.ExpectedVersion &&
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
	if owner.catalogs[input.AgentID].Version != input.ExpectedVersion {
		return errors.New("tools: catalog version conflict")
	}
	names := make(map[string]struct{}, len(input.Names))
	for _, name := range input.Names {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name || name == RunCodeName {
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
	if !slices.Contains(pending.update.Names, input.Schema.Name) {
		return errors.New("tools: candidate is not in the update")
	}
	parameters, err := SnapshotObjectSchema(input.Schema.Parameters)
	if err != nil {
		return err
	}
	input.Schema.Parameters = parameters
	if previous, exists := pending.candidates[input.Schema.Name]; exists {
		if previous.Visible == input.Visible && previous.Schema.Name == input.Schema.Name && previous.Schema.Description == input.Schema.Description && bytes.Equal(previous.Schema.Parameters, input.Schema.Parameters) {
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
func (owner *Registry) Publish(identifier UpdateID) (DefinitionsChanged, error) {
	pending, exists := owner.batches[identifier]
	if !exists {
		return DefinitionsChanged{}, errors.New("tools: unknown update")
	}
	if pending.published != nil {
		return clonePublication(*pending.published), nil
	}
	if owner.catalogs[pending.update.AgentID].Version != pending.update.ExpectedVersion {
		return DefinitionsChanged{}, errors.New("tools: catalog version conflict")
	}
	if len(pending.candidates) != len(pending.update.Names) {
		return DefinitionsChanged{}, errors.New("tools: candidate catalog is incomplete")
	}
	if pending.update.ExpectedVersion == ^uint64(0) {
		return DefinitionsChanged{}, errors.New("tools: catalog version exhausted")
	}
	definitions := Definitions{
		KnownNames: slices.Clone(pending.update.Names),
	}
	for _, name := range pending.update.Names {
		entry := pending.candidates[name]
		if entry.Visible {
			definitions.Schemas = append(definitions.Schemas, entry.Schema)
		}
	}
	committed := DefinitionsChanged{
		AgentID:     pending.update.AgentID,
		Version:     pending.update.ExpectedVersion + 1,
		Definitions: definitions,
	}
	if owner.catalogs == nil {
		owner.catalogs = make(map[session.SessionID]DefinitionsChanged)
	}
	owner.catalogs[committed.AgentID] = committed
	pending.published = &committed
	return clonePublication(committed), nil
}

// Read returns a detached committed baseline; candidates are never exposed.
func (owner *Registry) Read(identifier session.SessionID) (DefinitionsChanged, bool) {
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
	Catalog   DefinitionsChanged
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

func clonePublication(input DefinitionsChanged) DefinitionsChanged {
	input.Definitions.KnownNames = slices.Clone(input.Definitions.KnownNames)
	input.Definitions.Schemas = slices.Clone(input.Definitions.Schemas)
	for index := range input.Definitions.Schemas {
		input.Definitions.Schemas[index].Parameters = slices.Clone(input.Definitions.Schemas[index].Parameters)
	}
	return input
}
