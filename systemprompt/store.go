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
}

func newPromptStore() *promptStore {
	return &promptStore{
		sections:      make(map[string]PromptSection),
		contexts:      make(map[string]PromptContext),
		variables:     make(map[string]VariableProvider),
		toolProviders: make(map[string]ToolProvider),
		suppressors:   make(map[string]struct{}),
	}
}

func (storage *promptStore) addSection(definition PromptSection) error {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if _, exists := storage.sections[definition.Name]; exists {
		return fmt.Errorf(
			"systemprompt: prompt section %q is already registered in this layer",
			definition.Name,
		)
	}
	storage.sections[definition.Name] = definition
	storage.sectionOrder = append(storage.sectionOrder, definition.Name)
	return nil
}

func (storage *promptStore) removeSection(name string) bool {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if _, exists := storage.sections[name]; !exists {
		return false
	}
	delete(storage.sections, name)
	storage.sectionOrder = removeName(storage.sectionOrder, name)
	return true
}

func (storage *promptStore) addContext(definition PromptContext) error {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if _, exists := storage.contexts[definition.Name]; exists {
		return fmt.Errorf(
			"systemprompt: prompt context %q is already registered in this layer",
			definition.Name,
		)
	}
	storage.contexts[definition.Name] = definition
	storage.contextOrder = append(storage.contextOrder, definition.Name)
	return nil
}

func (storage *promptStore) removeContext(name string) bool {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if _, exists := storage.contexts[name]; !exists {
		return false
	}
	delete(storage.contexts, name)
	storage.contextOrder = removeName(storage.contextOrder, name)
	return true
}

func (storage *promptStore) addVariable(name string, provider VariableProvider) error {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if _, exists := storage.variables[name]; exists {
		return fmt.Errorf(
			"systemprompt: prompt variable %q is already registered in this layer",
			name,
		)
	}
	storage.variables[name] = provider
	storage.variableOrder = append(storage.variableOrder, name)
	return nil
}

func (storage *promptStore) removeVariable(name string) bool {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if _, exists := storage.variables[name]; !exists {
		return false
	}
	delete(storage.variables, name)
	storage.variableOrder = removeName(storage.variableOrder, name)
	return true
}

func (storage *promptStore) addToolProvider(name string, provider ToolProvider) error {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if _, exists := storage.toolProviders[name]; exists {
		return fmt.Errorf(
			"systemprompt: tool provider %q is already registered in this layer",
			name,
		)
	}
	storage.toolProviders[name] = provider
	storage.toolOrder = append(storage.toolOrder, name)
	return nil
}

func (storage *promptStore) removeToolProvider(name string) bool {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if _, exists := storage.toolProviders[name]; !exists {
		return false
	}
	delete(storage.toolProviders, name)
	storage.toolOrder = removeName(storage.toolOrder, name)
	return true
}

func (storage *promptStore) addSuppressor(name string) error {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if _, exists := storage.suppressors[name]; exists {
		return fmt.Errorf(
			"systemprompt: runtime-context suppressor %q is already registered in this layer",
			name,
		)
	}
	storage.suppressors[name] = struct{}{}
	return nil
}

func (storage *promptStore) removeSuppressor(name string) bool {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if _, exists := storage.suppressors[name]; !exists {
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
	storage.mutex.Unlock()
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
