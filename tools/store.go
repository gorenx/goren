package tools

import (
	"fmt"
	"sort"
	"sync"
)

type toolTable struct {
	byName map[string]*registeredTool
	order  []string
}

func (table *toolTable) add(entry *registeredTool) error {
	if table.byName == nil {
		table.byName = make(map[string]*registeredTool)
	}
	if _, exists := table.byName[entry.registrationName]; exists {
		return fmt.Errorf(
			"tools: tool %q is already registered in this layer",
			entry.registrationName,
		)
	}
	table.byName[entry.registrationName] = entry
	table.order = append(table.order, entry.registrationName)
	return nil
}

func (table *toolTable) remove(name string) (*registeredTool, int, bool) {
	entry := table.byName[name]
	if entry == nil {
		return nil, 0, false
	}
	delete(table.byName, name)
	entryIndex := -1
	for index, candidate := range table.order {
		if candidate == name {
			entryIndex = index
			break
		}
	}
	if entryIndex >= 0 {
		table.order = append(
			table.order[:entryIndex],
			table.order[entryIndex+1:]...,
		)
	}
	if len(table.byName) == 0 {
		table.byName = nil
		table.order = nil
	}
	return entry, entryIndex, true
}

func (table *toolTable) restore(entry *registeredTool, entryIndex int) error {
	if err := table.add(entry); err != nil {
		return err
	}
	lastIndex := len(table.order) - 1
	if entryIndex < 0 || entryIndex >= lastIndex {
		return nil
	}
	copy(table.order[entryIndex+1:], table.order[entryIndex:lastIndex])
	table.order[entryIndex] = entry.registrationName
	return nil
}

func (table *toolTable) entries() []*registeredTool {
	orderedEntries := make([]*registeredTool, 0, len(table.order))
	for _, name := range table.order {
		if entry := table.byName[name]; entry != nil {
			orderedEntries = append(orderedEntries, entry)
		}
	}
	return orderedEntries
}

func (table *toolTable) clear() {
	table.byName = nil
	table.order = nil
}

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

type restrictionEntry struct {
	name   string
	filter compiledRestriction
}

type guardEntry struct {
	name   string
	policy ToolGuard
}

type toolLayerSnapshot struct {
	tools        []*registeredTool
	restrictions []compiledRestriction
	guards       []ToolGuard
}

type toolView struct {
	visible          map[string]*registeredTool
	order            []string
	knownNames       map[string]struct{}
	restrictableName map[string]struct{}
}

type toolStore struct {
	mutex sync.RWMutex
	tools toolTable

	restrictions     map[string]compiledRestriction
	restrictionOrder []string
	guards           map[string]ToolGuard
	guardOrder       []string
}

func newToolStore() *toolStore {
	return &toolStore{}
}

func (storage *toolStore) addTool(entry *registeredTool) error {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	return storage.tools.add(entry)
}

func (storage *toolStore) removeTool(
	name string,
) (*registeredTool, int, bool) {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	return storage.tools.remove(name)
}

func (storage *toolStore) restoreTool(
	entry *registeredTool,
	entryIndex int,
) error {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	return storage.tools.restore(entry, entryIndex)
}

func (storage *toolStore) addRestriction(
	name string,
	filter compiledRestriction,
) error {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if storage.restrictions == nil {
		storage.restrictions = make(map[string]compiledRestriction)
	}
	if _, exists := storage.restrictions[name]; exists {
		return fmt.Errorf(
			"tools: restriction %q is already registered in this layer",
			name,
		)
	}
	storage.restrictions[name] = filter
	storage.restrictionOrder = append(storage.restrictionOrder, name)
	return nil
}

func (storage *toolStore) removeRestriction(
	name string,
) (compiledRestriction, int, bool) {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	filter, found := storage.restrictions[name]
	if !found {
		return compiledRestriction{}, 0, false
	}
	delete(storage.restrictions, name)
	entryIndex := removeOrderedName(&storage.restrictionOrder, name)
	if len(storage.restrictions) == 0 {
		storage.restrictions = nil
		storage.restrictionOrder = nil
	}
	return filter, entryIndex, true
}

func (storage *toolStore) restoreRestriction(
	name string,
	filter compiledRestriction,
	entryIndex int,
) error {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if storage.restrictions == nil {
		storage.restrictions = make(map[string]compiledRestriction)
	}
	if _, exists := storage.restrictions[name]; exists {
		return fmt.Errorf(
			"tools: restriction %q is already registered in this layer",
			name,
		)
	}
	storage.restrictions[name] = filter
	insertOrderedName(&storage.restrictionOrder, name, entryIndex)
	return nil
}

func (storage *toolStore) addGuard(name string, policy ToolGuard) error {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if storage.guards == nil {
		storage.guards = make(map[string]ToolGuard)
	}
	if _, exists := storage.guards[name]; exists {
		return fmt.Errorf(
			"tools: guard %q is already registered in this layer",
			name,
		)
	}
	storage.guards[name] = policy
	storage.guardOrder = append(storage.guardOrder, name)
	return nil
}

func (storage *toolStore) removeGuard(name string) bool {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	if _, found := storage.guards[name]; !found {
		return false
	}
	delete(storage.guards, name)
	removeOrderedName(&storage.guardOrder, name)
	if len(storage.guards) == 0 {
		storage.guards = nil
		storage.guardOrder = nil
	}
	return true
}

func (storage *toolStore) snapshot() toolLayerSnapshot {
	storage.mutex.RLock()
	defer storage.mutex.RUnlock()
	layer := toolLayerSnapshot{
		tools:        storage.tools.entries(),
		restrictions: make([]compiledRestriction, 0, len(storage.restrictionOrder)),
		guards:       make([]ToolGuard, 0, len(storage.guardOrder)),
	}
	for _, name := range storage.restrictionOrder {
		layer.restrictions = append(
			layer.restrictions,
			storage.restrictions[name],
		)
	}
	for _, name := range storage.guardOrder {
		layer.guards = append(layer.guards, storage.guards[name])
	}
	return layer
}

func (storage *toolStore) clear() {
	storage.mutex.Lock()
	storage.tools.clear()
	storage.restrictions = nil
	storage.restrictionOrder = nil
	storage.guards = nil
	storage.guardOrder = nil
	storage.mutex.Unlock()
}

func resolveToolView(layers []toolLayerSnapshot) toolView {
	resolved := toolView{
		visible:          make(map[string]*registeredTool),
		order:            make([]string, 0),
		knownNames:       make(map[string]struct{}),
		restrictableName: make(map[string]struct{}),
	}
	if len(layers) == 0 {
		return resolved
	}
	ownIndex := len(layers) - 1
	for layerIndex, layer := range layers {
		if layerIndex == ownIndex {
			for priorIndex := 0; priorIndex < ownIndex; priorIndex++ {
				for _, entry := range layers[priorIndex].tools {
					resolved.restrictableName[entry.registrationName] = struct{}{}
				}
			}
		}
		for _, filter := range layer.restrictions {
			resolved.order = removeDeniedTools(
				resolved.order,
				resolved.visible,
				filter,
			)
		}
		for _, entry := range layer.tools {
			name := entry.registrationName
			resolved.knownNames[name] = struct{}{}
			if _, exists := resolved.visible[name]; !exists {
				resolved.order = append(resolved.order, name)
			}
			resolved.visible[name] = entry
		}
	}
	return resolved
}

func removeDeniedTools(
	order []string,
	visible map[string]*registeredTool,
	filter compiledRestriction,
) []string {
	retainedOrder := order[:0]
	for _, name := range order {
		if filter.admits(name) {
			retainedOrder = append(retainedOrder, name)
			continue
		}
		delete(visible, name)
	}
	return retainedOrder
}

func guardPolicies(layers []toolLayerSnapshot) []ToolGuard {
	policies := make([]ToolGuard, 0)
	for _, layer := range layers {
		policies = append(policies, layer.guards...)
	}
	return policies
}

func removeOrderedName(order *[]string, name string) int {
	for index, candidate := range *order {
		if candidate != name {
			continue
		}
		*order = append((*order)[:index], (*order)[index+1:]...)
		return index
	}
	return -1
}

func insertOrderedName(order *[]string, name string, entryIndex int) {
	if entryIndex < 0 || entryIndex >= len(*order) {
		*order = append(*order, name)
		return
	}
	*order = append(*order, "")
	copy((*order)[entryIndex+1:], (*order)[entryIndex:])
	(*order)[entryIndex] = name
}

func sortedNames(names map[string]struct{}) []string {
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	return ordered
}
