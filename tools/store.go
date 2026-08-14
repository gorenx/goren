package tools

import (
	"fmt"
	"slices"
	"sort"
	"sync"

	"github.com/gorenx/goren/plugin"
)

type toolRecord struct {
	name   string
	entry  *registeredTool
	active bool
}

type toolTable struct {
	byName map[string]*toolRecord
	names  []string
}

func (storage *toolTable) add(entry *registeredTool, duplicateDetail string) (*toolRecord, error) {
	if storage.byName == nil {
		storage.byName = make(map[string]*toolRecord)
	}
	if _, exists := storage.byName[entry.registrationName]; exists {
		return nil, fmt.Errorf("%s", duplicateDetail)
	}
	record := &toolRecord{name: entry.registrationName, entry: entry, active: true}
	storage.byName[record.name] = record
	storage.names = append(storage.names, record.name)
	return record, nil
}

func (storage *toolTable) remove(record *toolRecord) {
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

func (storage *toolTable) entries() []*registeredTool {
	definitions := make([]*registeredTool, 0, len(storage.names))
	for _, name := range storage.names {
		record := storage.byName[name]
		if record != nil && record.active {
			definitions = append(definitions, record.entry)
		}
	}
	return definitions
}

func (storage *toolTable) empty() bool { return len(storage.byName) == 0 }

type compiledRestriction struct {
	allow map[string]struct{}
	deny  map[string]struct{}
}

func (filter compiledRestriction) admits(name string) bool {
	if filter.allow != nil {
		if _, exists := filter.allow[name]; !exists {
			return false
		}
	}
	if _, denied := filter.deny[name]; denied {
		return false
	}
	return true
}

type restrictionRecord struct {
	identity uint64
	filter   compiledRestriction
	active   bool
}

type guardRecord struct {
	identity uint64
	policy   ToolGuard
	active   bool
}

type toolLayer struct {
	tools        toolTable
	restrictions []*restrictionRecord
	guards       []*guardRecord
}

func (layer *toolLayer) empty() bool {
	return layer.tools.empty() && len(layer.restrictions) == 0 && len(layer.guards) == 0
}

func (layer *toolLayer) admits(name string) bool {
	for _, record := range layer.restrictions {
		if record.active && !record.filter.admits(name) {
			return false
		}
	}
	return true
}

type toolView struct {
	visible          map[string]*registeredTool
	order            []string
	knownNames       map[string]struct{}
	restrictableName map[string]struct{}
}

type toolStore struct {
	mu     sync.Mutex
	global toolLayer
	scoped map[plugin.ScopeKey]*toolLayer
	nextID uint64
}

func newToolStore() *toolStore {
	return &toolStore{scoped: make(map[plugin.ScopeKey]*toolLayer)}
}

func (storage *toolStore) addTool(selectedKey plugin.ScopeKey, entry *registeredTool) (func(), error) {
	storage.mu.Lock()
	layer := storage.layerLocked(selectedKey)
	detail := duplicateToolMessage(selectedKey, entry.registrationName)
	record, err := layer.tools.add(entry, detail)
	storage.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return func() {
		storage.mu.Lock()
		layer.tools.remove(record)
		storage.pruneLocked(selectedKey)
		storage.mu.Unlock()
	}, nil
}

func (storage *toolStore) addRestriction(selectedKey plugin.ScopeKey, filter compiledRestriction) func() {
	storage.mu.Lock()
	storage.nextID++
	layer := storage.layerLocked(selectedKey)
	record := &restrictionRecord{identity: storage.nextID, filter: filter, active: true}
	layer.restrictions = append(layer.restrictions, record)
	storage.mu.Unlock()
	return func() {
		storage.mu.Lock()
		if record.active {
			record.active = false
			layer.restrictions = slices.DeleteFunc(layer.restrictions, func(candidate *restrictionRecord) bool {
				return candidate == record
			})
		}
		storage.pruneLocked(selectedKey)
		storage.mu.Unlock()
	}
}

func (storage *toolStore) addGuard(selectedKey plugin.ScopeKey, policy ToolGuard) func() {
	storage.mu.Lock()
	storage.nextID++
	layer := storage.layerLocked(selectedKey)
	record := &guardRecord{identity: storage.nextID, policy: policy, active: true}
	layer.guards = append(layer.guards, record)
	storage.mu.Unlock()
	return func() {
		storage.mu.Lock()
		if record.active {
			record.active = false
			layer.guards = slices.DeleteFunc(layer.guards, func(candidate *guardRecord) bool { return candidate == record })
		}
		storage.pruneLocked(selectedKey)
		storage.mu.Unlock()
	}
}

func (storage *toolStore) view(selectedKey plugin.ScopeKey) toolView {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	layers, own := storage.layersLocked(selectedKey)
	inherited := make(map[string]*registeredTool)
	order := make([]string, 0)
	retain := func(entry *registeredTool) {
		name := entry.registrationName
		if _, exists := inherited[name]; !exists {
			order = append(order, name)
		}
		inherited[name] = entry
	}
	for _, entry := range storage.global.tools.entries() {
		retain(entry)
	}
	for _, layer := range layers {
		if layer == own {
			continue
		}
		for _, entry := range layer.tools.entries() {
			retain(entry)
		}
	}
	resolved := toolView{
		visible: make(map[string]*registeredTool), knownNames: make(map[string]struct{}),
		restrictableName: make(map[string]struct{}), order: make([]string, 0, len(order)),
	}
	for _, name := range order {
		resolved.knownNames[name] = struct{}{}
		resolved.restrictableName[name] = struct{}{}
		admitted := true
		for _, layer := range layers {
			if !layer.admits(name) {
				admitted = false
				break
			}
		}
		if admitted {
			resolved.visible[name] = inherited[name]
			resolved.order = append(resolved.order, name)
		}
	}
	if own != nil {
		for _, entry := range own.tools.entries() {
			name := entry.registrationName
			if _, exists := resolved.visible[name]; !exists {
				resolved.order = append(resolved.order, name)
			}
			resolved.knownNames[name] = struct{}{}
			resolved.visible[name] = entry
		}
	}
	return resolved
}

func (storage *toolStore) guards(selectedKey plugin.ScopeKey) []ToolGuard {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	policies := make([]ToolGuard, 0)
	for _, record := range storage.global.guards {
		if record.active {
			policies = append(policies, record.policy)
		}
	}
	layers, _ := storage.layersLocked(selectedKey)
	for _, layer := range layers {
		for _, record := range layer.guards {
			if record.active {
				policies = append(policies, record.policy)
			}
		}
	}
	return policies
}

func (storage *toolStore) layerLocked(selectedKey plugin.ScopeKey) *toolLayer {
	if selectedKey.IsGlobal() {
		return &storage.global
	}
	layer := storage.scoped[selectedKey]
	if layer == nil {
		layer = &toolLayer{}
		storage.scoped[selectedKey] = layer
	}
	return layer
}

func (storage *toolStore) layersLocked(selectedKey plugin.ScopeKey) ([]*toolLayer, *toolLayer) {
	layers := make([]*toolLayer, 0)
	var own *toolLayer
	for _, lineageKey := range plugin.ScopeLineage(selectedKey) {
		if layer := storage.scoped[lineageKey]; layer != nil {
			layers = append(layers, layer)
			if lineageKey == selectedKey {
				own = layer
			}
		}
	}
	return layers, own
}

func (storage *toolStore) pruneLocked(selectedKey plugin.ScopeKey) {
	if selectedKey.IsGlobal() {
		return
	}
	if layer := storage.scoped[selectedKey]; layer != nil && layer.empty() {
		delete(storage.scoped, selectedKey)
	}
}

func duplicateToolMessage(selectedKey plugin.ScopeKey, name string) string {
	if selectedKey.IsGlobal() {
		return fmt.Sprintf("tools: tool %q is already registered (for a per-agent variant, register through that agent's scope instead)", name)
	}
	return fmt.Sprintf("tools: tool %q is already registered in this scope", name)
}

func sortedNames(names map[string]struct{}) []string {
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	return ordered
}
