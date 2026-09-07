package systemprompt

import (
	"errors"
	"fmt"
	"github.com/google/uuid"
	"slices"
)

type namedVariableProvider struct {
	name     string
	provider VariableProvider
}

type promptEntryKind uint8

const (
	promptSectionEntry promptEntryKind = iota + 1
	promptContextEntry
	promptVariableEntry
	promptSuppressorEntry
)

type promptEntryKey struct {
	kind promptEntryKind
	name string
}

type promptLayerSnapshot struct {
	sections  []PromptSection
	contexts  []PromptContext
	variables []namedVariableProvider
	// contextSuppressed disables runtime-context providers for this captured layer.
	// It is independent of Go context cancellation and does not hide tool schemas.
	contextSuppressed bool
}

// promptStore owns local registrations. The owning Grain serializes mutations
// and capture; assembly tasks read only the immutable snapshots it produces.
type promptStore struct {
	snapshotValue promptLayerSnapshot

	sections      map[string]PromptSection
	sectionOrder  []string
	contexts      map[string]PromptContext
	contextOrder  []string
	variables     map[string]VariableProvider
	variableOrder []string
	suppressors   map[string]struct{}
	entries       map[RegistrationID]promptEntryKey
}

func newPromptStore() *promptStore {
	storage := &promptStore{}
	storage.rebuildSnapshot()
	return storage
}

func (storage *promptStore) addSection(
	definition PromptSection,
) (RegistrationID, error) {
	if _, exists := storage.sections[definition.Name]; exists {
		return "", fmt.Errorf(
			"systemprompt: prompt section %q is already registered in this layer",
			definition.Name,
		)
	}
	identifier := storage.addEntry(promptSectionEntry, definition.Name)
	if storage.sections == nil {
		storage.sections = make(map[string]PromptSection)
	}
	storage.sections[definition.Name] = definition
	storage.sectionOrder = append(storage.sectionOrder, definition.Name)
	storage.rebuildSnapshot()
	return identifier, nil
}

func (storage *promptStore) addContext(
	definition PromptContext,
) (RegistrationID, error) {
	if _, exists := storage.contexts[definition.Name]; exists {
		return "", fmt.Errorf(
			"systemprompt: prompt context %q is already registered in this layer",
			definition.Name,
		)
	}
	identifier := storage.addEntry(promptContextEntry, definition.Name)
	if storage.contexts == nil {
		storage.contexts = make(map[string]PromptContext)
	}
	storage.contexts[definition.Name] = definition
	storage.contextOrder = append(storage.contextOrder, definition.Name)
	storage.rebuildSnapshot()
	return identifier, nil
}

func (storage *promptStore) addVariable(
	name string,
	provider VariableProvider,
) (RegistrationID, error) {
	if _, exists := storage.variables[name]; exists {
		return "", fmt.Errorf(
			"systemprompt: prompt variable %q is already registered in this layer",
			name,
		)
	}
	identifier := storage.addEntry(promptVariableEntry, name)
	if storage.variables == nil {
		storage.variables = make(map[string]VariableProvider)
	}
	storage.variables[name] = provider
	storage.variableOrder = append(storage.variableOrder, name)
	storage.rebuildSnapshot()
	return identifier, nil
}

func (storage *promptStore) addSuppressor(
	name string,
) (RegistrationID, error) {
	if _, exists := storage.suppressors[name]; exists {
		return "", fmt.Errorf(
			"systemprompt: runtime-context suppressor %q is already registered in this layer",
			name,
		)
	}
	identifier := storage.addEntry(promptSuppressorEntry, name)
	if storage.suppressors == nil {
		storage.suppressors = make(map[string]struct{})
	}
	storage.suppressors[name] = struct{}{}
	storage.rebuildSnapshot()
	return identifier, nil
}

func (storage *promptStore) capture() promptLayerSnapshot {
	captured := storage.snapshotValue
	return captured
}

// rebuildSnapshot moves immutable registration-view construction to
// mutation time while keeping provider resolution in every Assemble call.
func (storage *promptStore) rebuildSnapshot() {
	storage.snapshotValue = promptLayerSnapshot{
		sections:          make([]PromptSection, 0, len(storage.sections)),
		contexts:          make([]PromptContext, 0, len(storage.contexts)),
		variables:         make([]namedVariableProvider, 0, len(storage.variables)),
		contextSuppressed: len(storage.suppressors) != 0,
	}
	for _, name := range storage.sectionOrder {
		if definition, exists := storage.sections[name]; exists {
			storage.snapshotValue.sections = append(
				storage.snapshotValue.sections,
				definition,
			)
		}
	}
	for _, name := range storage.contextOrder {
		if definition, exists := storage.contexts[name]; exists {
			storage.snapshotValue.contexts = append(
				storage.snapshotValue.contexts,
				definition,
			)
		}
	}
	for _, name := range storage.variableOrder {
		if provider, exists := storage.variables[name]; exists {
			storage.snapshotValue.variables = append(
				storage.snapshotValue.variables,
				namedVariableProvider{
					name:     name,
					provider: provider,
				},
			)
		}
	}

}

func (storage *promptStore) clear() {
	storage.sections = nil
	storage.sectionOrder = nil
	storage.contexts = nil
	storage.contextOrder = nil
	storage.variables = nil
	storage.variableOrder = nil
	storage.suppressors = nil
	storage.entries = nil
	storage.rebuildSnapshot()
}

func (storage *promptStore) addEntry(kind promptEntryKind, name string) RegistrationID {
	if storage.entries == nil {
		storage.entries = make(map[RegistrationID]promptEntryKey)
	}
	identifier := RegistrationID(uuid.NewString())
	storage.entries[identifier] = promptEntryKey{
		kind: kind,
		name: name,
	}
	return identifier
}

func (storage *promptStore) remove(identifier RegistrationID) bool {
	entry, found := storage.entries[identifier]
	if !found {
		return false
	}
	switch entry.kind {
	case promptSectionEntry:
		delete(storage.sections, entry.name)
		storage.sectionOrder = removeName(storage.sectionOrder, entry.name)
	case promptContextEntry:
		delete(storage.contexts, entry.name)
		storage.contextOrder = removeName(storage.contextOrder, entry.name)
	case promptVariableEntry:
		delete(storage.variables, entry.name)
		storage.variableOrder = removeName(storage.variableOrder, entry.name)
	case promptSuppressorEntry:
		delete(storage.suppressors, entry.name)
	}
	delete(storage.entries, identifier)
	storage.rebuildSnapshot()
	return true
}

func removeName(names []string, selectedName string) []string {
	return slices.DeleteFunc(names, func(candidate string) bool {
		return candidate == selectedName
	})
}

func validateEntryName(kind string, name string) error {
	if name == "" {
		return errors.New("systemprompt: " + kind + " name must be non-empty")
	}
	return nil
}
