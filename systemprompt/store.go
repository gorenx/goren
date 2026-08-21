package systemprompt

import (
	"errors"
	"fmt"
	"slices"
	"sync"
)

type namedVariableProvider struct {
	name     string
	provider VariableProvider
}

type namedToolProvider struct {
	name     string
	provider ToolProvider
}

type promptEntryKind uint8

const (
	promptSectionEntry promptEntryKind = iota + 1
	promptContextEntry
	promptVariableEntry
	promptToolProviderEntry
	promptSuppressorEntry
)

type promptEntryKey struct {
	kind promptEntryKind
	name string
}

type promptEntryToken struct {
	marker byte
}

type promptLayerSnapshot struct {
	sections          []PromptSection
	contexts          []PromptContext
	variables         []namedVariableProvider
	toolProviders     []namedToolProvider
	contextSuppressed bool
}

// promptStore owns one exact System Prompt layer. Parent/child composition is
// performed by Registry, so this object never sees Plugin Scope identities.
type promptStore struct {
	mutex sync.RWMutex

	sections      map[string]PromptSection
	sectionOrder  []string
	contexts      map[string]PromptContext
	contextOrder  []string
	variables     map[string]VariableProvider
	variableOrder []string
	toolProviders map[string]ToolProvider
	toolOrder     []string
	suppressors   map[string]struct{}
	tokens        map[promptEntryKey]*promptEntryToken
}

func newPromptStore() *promptStore {
	return &promptStore{
		sections:      make(map[string]PromptSection),
		contexts:      make(map[string]PromptContext),
		variables:     make(map[string]VariableProvider),
		toolProviders: make(map[string]ToolProvider),
		suppressors:   make(map[string]struct{}),
		tokens:        make(map[promptEntryKey]*promptEntryToken),
	}
}

func (storage *promptStore) addSection(
	definition PromptSection,
) (*promptEntryToken, error) {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if _, exists := storage.sections[definition.Name]; exists {
		return nil, fmt.Errorf(
			"systemprompt: prompt section %q is already registered in this layer",
			definition.Name,
		)
	}
	token := storage.addToken(promptSectionEntry, definition.Name)
	storage.sections[definition.Name] = definition
	storage.sectionOrder = append(storage.sectionOrder, definition.Name)
	return token, nil
}

func (storage *promptStore) removeSection(
	name string,
	expected *promptEntryToken,
) bool {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if !storage.removeToken(promptSectionEntry, name, expected) {
		return false
	}
	delete(storage.sections, name)
	storage.sectionOrder = removeName(storage.sectionOrder, name)
	return true
}

func (storage *promptStore) addContext(
	definition PromptContext,
) (*promptEntryToken, error) {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if _, exists := storage.contexts[definition.Name]; exists {
		return nil, fmt.Errorf(
			"systemprompt: prompt context %q is already registered in this layer",
			definition.Name,
		)
	}
	token := storage.addToken(promptContextEntry, definition.Name)
	storage.contexts[definition.Name] = definition
	storage.contextOrder = append(storage.contextOrder, definition.Name)
	return token, nil
}

func (storage *promptStore) removeContext(
	name string,
	expected *promptEntryToken,
) bool {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if !storage.removeToken(promptContextEntry, name, expected) {
		return false
	}
	delete(storage.contexts, name)
	storage.contextOrder = removeName(storage.contextOrder, name)
	return true
}

func (storage *promptStore) addVariable(
	name string,
	provider VariableProvider,
) (*promptEntryToken, error) {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if _, exists := storage.variables[name]; exists {
		return nil, fmt.Errorf(
			"systemprompt: prompt variable %q is already registered in this layer",
			name,
		)
	}
	token := storage.addToken(promptVariableEntry, name)
	storage.variables[name] = provider
	storage.variableOrder = append(storage.variableOrder, name)
	return token, nil
}

func (storage *promptStore) removeVariable(
	name string,
	expected *promptEntryToken,
) bool {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if !storage.removeToken(promptVariableEntry, name, expected) {
		return false
	}
	delete(storage.variables, name)
	storage.variableOrder = removeName(storage.variableOrder, name)
	return true
}

func (storage *promptStore) addToolProvider(
	name string,
	provider ToolProvider,
) (*promptEntryToken, error) {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if _, exists := storage.toolProviders[name]; exists {
		return nil, fmt.Errorf(
			"systemprompt: tool provider %q is already registered in this layer",
			name,
		)
	}
	token := storage.addToken(promptToolProviderEntry, name)
	storage.toolProviders[name] = provider
	storage.toolOrder = append(storage.toolOrder, name)
	return token, nil
}

func (storage *promptStore) removeToolProvider(
	name string,
	expected *promptEntryToken,
) bool {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if !storage.removeToken(promptToolProviderEntry, name, expected) {
		return false
	}
	delete(storage.toolProviders, name)
	storage.toolOrder = removeName(storage.toolOrder, name)
	return true
}

func (storage *promptStore) addSuppressor(
	name string,
) (*promptEntryToken, error) {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if _, exists := storage.suppressors[name]; exists {
		return nil, fmt.Errorf(
			"systemprompt: runtime-context suppressor %q is already registered in this layer",
			name,
		)
	}
	token := storage.addToken(promptSuppressorEntry, name)
	storage.suppressors[name] = struct{}{}
	return token, nil
}

func (storage *promptStore) removeSuppressor(
	name string,
	expected *promptEntryToken,
) bool {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if !storage.removeToken(promptSuppressorEntry, name, expected) {
		return false
	}
	delete(storage.suppressors, name)
	return true
}

func (storage *promptStore) capture() promptLayerSnapshot {
	storage.mutex.RLock()
	defer storage.mutex.RUnlock()
	snapshot := promptLayerSnapshot{
		sections:          make([]PromptSection, 0, len(storage.sections)),
		contexts:          make([]PromptContext, 0, len(storage.contexts)),
		variables:         make([]namedVariableProvider, 0, len(storage.variables)),
		toolProviders:     make([]namedToolProvider, 0, len(storage.toolProviders)),
		contextSuppressed: len(storage.suppressors) != 0,
	}
	for _, name := range storage.sectionOrder {
		if definition, exists := storage.sections[name]; exists {
			snapshot.sections = append(snapshot.sections, definition)
		}
	}
	for _, name := range storage.contextOrder {
		if definition, exists := storage.contexts[name]; exists {
			snapshot.contexts = append(snapshot.contexts, definition)
		}
	}
	for _, name := range storage.variableOrder {
		if provider, exists := storage.variables[name]; exists {
			snapshot.variables = append(snapshot.variables, namedVariableProvider{
				name:     name,
				provider: provider,
			})
		}
	}
	for _, name := range storage.toolOrder {
		if provider, exists := storage.toolProviders[name]; exists {
			snapshot.toolProviders = append(snapshot.toolProviders, namedToolProvider{
				name:     name,
				provider: provider,
			})
		}
	}
	return snapshot
}

func (storage *promptStore) clear() {
	storage.mutex.Lock()
	storage.sections = make(map[string]PromptSection)
	storage.sectionOrder = nil
	storage.contexts = make(map[string]PromptContext)
	storage.contextOrder = nil
	storage.variables = make(map[string]VariableProvider)
	storage.variableOrder = nil
	storage.toolProviders = make(map[string]ToolProvider)
	storage.toolOrder = nil
	storage.suppressors = make(map[string]struct{})
	storage.tokens = make(map[promptEntryKey]*promptEntryToken)
	storage.mutex.Unlock()
}

func (storage *promptStore) addToken(
	kind promptEntryKind,
	name string,
) *promptEntryToken {
	token := &promptEntryToken{}
	storage.tokens[promptEntryKey{
		kind: kind,
		name: name,
	}] = token
	return token
}

func (storage *promptStore) removeToken(
	kind promptEntryKind,
	name string,
	expected *promptEntryToken,
) bool {
	key := promptEntryKey{
		kind: kind,
		name: name,
	}
	if expected == nil || storage.tokens[key] != expected {
		return false
	}
	delete(storage.tokens, key)
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
