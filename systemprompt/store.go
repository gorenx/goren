package systemprompt

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"

	"github.com/gorenx/goren/plugin"
)

type namedContribution interface {
	PromptSection | PromptContext | VariableProvider
}

type anonymousContribution interface {
	struct{} | ToolProvider
}

type namedRecord[T namedContribution] struct {
	name     string
	retained T
	active   bool
}

type namedTable[T namedContribution] struct {
	byName map[string]*namedRecord[T]
	names  []string
}

func (storage *namedTable[T]) add(name string, retained T, duplicateDetail string) (*namedRecord[T], error) {
	if storage.byName == nil {
		storage.byName = make(map[string]*namedRecord[T])
	}
	if _, exists := storage.byName[name]; exists {
		return nil, errors.New(duplicateDetail)
	}
	record := &namedRecord[T]{name: name, retained: retained, active: true}
	storage.byName[name] = record
	storage.names = append(storage.names, name)
	return record, nil
}

func (storage *namedTable[T]) remove(record *namedRecord[T]) {
	if record == nil || !record.active || storage.byName[record.name] != record {
		return
	}
	record.active = false
	delete(storage.byName, record.name)
	storage.names = slices.DeleteFunc(storage.names, func(candidate string) bool { return candidate == record.name })
	if len(storage.byName) == 0 {
		storage.byName = nil
		storage.names = nil
	}
}

func (storage *namedTable[T]) entries() []namedItem[T] {
	items := make([]namedItem[T], 0, len(storage.names))
	for _, name := range storage.names {
		record := storage.byName[name]
		if record != nil && record.active {
			items = append(items, namedItem[T]{name: name, retained: record.retained})
		}
	}
	return items
}

func (storage *namedTable[T]) empty() bool {
	return len(storage.byName) == 0
}

type namedItem[T namedContribution] struct {
	name     string
	retained T
}

type anonymousRecord[T anonymousContribution] struct {
	id       uint64
	retained T
	active   bool
}

type anonymousTable[T anonymousContribution] struct {
	byID  map[uint64]*anonymousRecord[T]
	order []uint64
}

func (storage *anonymousTable[T]) add(identity uint64, retained T) *anonymousRecord[T] {
	if storage.byID == nil {
		storage.byID = make(map[uint64]*anonymousRecord[T])
	}
	record := &anonymousRecord[T]{id: identity, retained: retained, active: true}
	storage.byID[identity] = record
	storage.order = append(storage.order, identity)
	return record
}

func (storage *anonymousTable[T]) remove(record *anonymousRecord[T]) {
	if record == nil || !record.active || storage.byID[record.id] != record {
		return
	}
	record.active = false
	delete(storage.byID, record.id)
	storage.order = slices.DeleteFunc(storage.order, func(candidate uint64) bool { return candidate == record.id })
	if len(storage.byID) == 0 {
		storage.byID = nil
		storage.order = nil
	}
}

func (storage *anonymousTable[T]) values() []T {
	retainedValues := make([]T, 0, len(storage.order))
	for _, identity := range storage.order {
		record := storage.byID[identity]
		if record != nil && record.active {
			retainedValues = append(retainedValues, record.retained)
		}
	}
	return retainedValues
}

func (storage *anonymousTable[T]) empty() bool {
	return len(storage.byID) == 0
}

type promptLayer struct {
	sections    namedTable[PromptSection]
	contexts    namedTable[PromptContext]
	variables   namedTable[VariableProvider]
	suppressors anonymousTable[struct{}]
	providers   anonymousTable[ToolProvider]
}

func (layer *promptLayer) empty() bool {
	return layer.sections.empty() && layer.contexts.empty() && layer.variables.empty() &&
		layer.suppressors.empty() && layer.providers.empty()
}

type promptSnapshot struct {
	sections          []PromptSection
	contexts          []PromptContext
	variableProviders [][]namedItem[VariableProvider]
	toolProviders     []ToolProvider
	contextSuppressed bool
}

// promptStore owns mutable global/scoped contribution state. Provider
// evaluation never runs while its lock is held; capture returns a membership
// snapshot consumed by promptAssembler.
type promptStore struct {
	mu     sync.Mutex
	global promptLayer
	scoped map[plugin.ScopeKey]*promptLayer
	nextID uint64
}

func newPromptStore() *promptStore {
	return &promptStore{scoped: make(map[plugin.ScopeKey]*promptLayer)}
}

func (storage *promptStore) addSection(selectedKey plugin.ScopeKey, definition PromptSection) (func(), error) {
	storage.mu.Lock()
	layer := storage.layerLocked(selectedKey)
	record, err := layer.sections.add(definition.Name, definition, duplicateMessage(selectedKey, "section", definition.Name))
	storage.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return func() {
		storage.mu.Lock()
		layer.sections.remove(record)
		storage.pruneLocked(selectedKey)
		storage.mu.Unlock()
	}, nil
}

func (storage *promptStore) addContext(selectedKey plugin.ScopeKey, definition PromptContext) (func(), error) {
	storage.mu.Lock()
	layer := storage.layerLocked(selectedKey)
	record, err := layer.contexts.add(definition.Name, definition, duplicateMessage(selectedKey, "context", definition.Name))
	storage.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return func() {
		storage.mu.Lock()
		layer.contexts.remove(record)
		storage.pruneLocked(selectedKey)
		storage.mu.Unlock()
	}, nil
}

func (storage *promptStore) addSuppressor(selectedKey plugin.ScopeKey) func() {
	storage.mu.Lock()
	storage.nextID++
	layer := storage.layerLocked(selectedKey)
	record := layer.suppressors.add(storage.nextID, struct{}{})
	storage.mu.Unlock()
	return func() {
		storage.mu.Lock()
		layer.suppressors.remove(record)
		storage.pruneLocked(selectedKey)
		storage.mu.Unlock()
	}
}

func (storage *promptStore) addToolProvider(selectedKey plugin.ScopeKey, callback ToolProvider) func() {
	storage.mu.Lock()
	storage.nextID++
	layer := storage.layerLocked(selectedKey)
	record := layer.providers.add(storage.nextID, callback)
	storage.mu.Unlock()
	return func() {
		storage.mu.Lock()
		layer.providers.remove(record)
		storage.pruneLocked(selectedKey)
		storage.mu.Unlock()
	}
}

func (storage *promptStore) addVariable(selectedKey plugin.ScopeKey, name string, callback VariableProvider) (func(), error) {
	storage.mu.Lock()
	layer := storage.layerLocked(selectedKey)
	record, err := layer.variables.add(name, callback, duplicateMessage(selectedKey, "variable", name))
	storage.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return func() {
		storage.mu.Lock()
		layer.variables.remove(record)
		storage.pruneLocked(selectedKey)
		storage.mu.Unlock()
	}, nil
}

func (storage *promptStore) capture(selectedKey plugin.ScopeKey) promptSnapshot {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	layers := make([]*promptLayer, 0)
	for _, lineageKey := range plugin.ScopeLineage(selectedKey) {
		if layer := storage.scoped[lineageKey]; layer != nil {
			layers = append(layers, layer)
		}
	}
	state := promptSnapshot{
		sections: mergeNamed(storage.global.sections.entries(), layers, func(layer *promptLayer) []namedItem[PromptSection] {
			return layer.sections.entries()
		}),
		contexts: mergeNamed(storage.global.contexts.entries(), layers, func(layer *promptLayer) []namedItem[PromptContext] {
			return layer.contexts.entries()
		}),
		variableProviders: make([][]namedItem[VariableProvider], 0, len(layers)+1),
		toolProviders:     storage.global.providers.values(),
		contextSuppressed: !storage.global.suppressors.empty(),
	}
	state.variableProviders = append(state.variableProviders, storage.global.variables.entries())
	for _, layer := range layers {
		state.variableProviders = append(state.variableProviders, layer.variables.entries())
		state.toolProviders = append(state.toolProviders, layer.providers.values()...)
		state.contextSuppressed = state.contextSuppressed || !layer.suppressors.empty()
	}
	sort.SliceStable(state.sections, func(leftIndex int, rightIndex int) bool {
		return state.sections[leftIndex].Order < state.sections[rightIndex].Order
	})
	sort.SliceStable(state.contexts, func(leftIndex int, rightIndex int) bool {
		return state.contexts[leftIndex].Order < state.contexts[rightIndex].Order
	})
	return state
}

func (storage *promptStore) layerLocked(selectedKey plugin.ScopeKey) *promptLayer {
	if selectedKey.IsGlobal() {
		return &storage.global
	}
	layer := storage.scoped[selectedKey]
	if layer == nil {
		layer = &promptLayer{}
		storage.scoped[selectedKey] = layer
	}
	return layer
}

func (storage *promptStore) pruneLocked(selectedKey plugin.ScopeKey) {
	if selectedKey.IsGlobal() {
		return
	}
	if layer := storage.scoped[selectedKey]; layer != nil && layer.empty() {
		delete(storage.scoped, selectedKey)
	}
}

func duplicateMessage(selectedKey plugin.ScopeKey, kind string, name string) string {
	if selectedKey.IsGlobal() {
		return fmt.Sprintf("systemprompt: prompt %s %q is already registered (for a per-agent override, register through that agent's scope instead)", kind, name)
	}
	return fmt.Sprintf("systemprompt: prompt %s %q is already registered in this scope", kind, name)
}

func mergeNamed[T namedContribution](globalEntries []namedItem[T], layers []*promptLayer, pick func(*promptLayer) []namedItem[T]) []T {
	names := make([]string, 0, len(globalEntries))
	byName := make(map[string]T)
	for _, item := range globalEntries {
		names = append(names, item.name)
		byName[item.name] = item.retained
	}
	for _, layer := range layers {
		for _, item := range pick(layer) {
			if _, exists := byName[item.name]; !exists {
				names = append(names, item.name)
			}
			byName[item.name] = item.retained
		}
	}
	merged := make([]T, 0, len(names))
	for _, name := range names {
		merged = append(merged, byName[name])
	}
	return merged
}
