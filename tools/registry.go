package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/systemprompt"
)

type registryLayerSource interface {
	toolLayers() []toolLayerSnapshot
}

// registry owns one exact layer of Tool definitions and policies. It has no
// Plugin lifecycle and never executes a Tool; Service publishes its mutations
// and Runtime consumes its read view.
type registry struct {
	root   bool
	store  *toolStore
	parent registryLayerSource

	viewMutex sync.RWMutex
	viewCache cachedToolView
}

type cachedToolView struct {
	layers []toolLayerVersion
	view   toolView
}

type toolLayerVersion struct {
	source   *toolStore
	revision uint64
}

func newRegistry(root bool) *registry {
	return &registry{
		root:  root,
		store: newToolStore(),
	}
}

func (catalog *registry) attachParent(parent registryLayerSource) error {
	if catalog.root {
		return errors.New("tools: root Registry cannot have a parent")
	}
	if parent == nil {
		return errors.New("tools: child Registry requires a parent")
	}
	catalog.parent = parent
	return nil
}

func (catalog *registry) clear() {
	catalog.parent = nil
	catalog.store.clear()
}

func (catalog *registry) compileTool(definition ToolDefinition) (*registeredTool, error) {
	return compileDefinition(definition)
}

func (catalog *registry) addTool(entry *registeredTool) error {
	return catalog.store.addTool(entry)
}

func (catalog *registry) removeTool(
	name string,
	expected *registeredTool,
) bool {
	return catalog.store.removeTool(name, expected)
}

func (catalog *registry) compileRestriction(restriction ToolRestriction) (compiledRestriction, error) {
	if catalog.root {
		return compiledRestriction{}, errors.New(
			"tools: restrictions require a child Registry layer",
		)
	}
	if restriction.Allow == nil && restriction.Deny == nil {
		return compiledRestriction{}, errors.New(
			"tools: restriction requires allow and/or deny",
		)
	}
	return compileRestriction(
		restriction,
		catalog.view().restrictableName,
	)
}

func (catalog *registry) addRestriction(
	name string,
	restriction compiledRestriction,
) (*registeredRestriction, error) {
	return catalog.store.addRestriction(name, restriction)
}

func (catalog *registry) removeRestriction(
	name string,
	expected *registeredRestriction,
) bool {
	return catalog.store.removeRestriction(name, expected)
}

func (catalog *registry) addGuard(
	name string,
	policy ToolGuard,
) (*registeredGuard, error) {
	if policy == nil {
		return nil, fmt.Errorf("tools: guard %q policy is nil", name)
	}
	return catalog.store.addGuard(name, policy)
}

func (catalog *registry) removeGuard(
	name string,
	expected *registeredGuard,
) bool {
	return catalog.store.removeGuard(name, expected)
}

func (catalog *registry) find(name string) (*registeredTool, bool) {
	entry, found := catalog.view().visible[name]
	return entry, found
}

func (catalog *registry) lookupDefinition(name string) (ToolDefinition, bool) {
	entry, found := catalog.find(name)
	if !found {
		return ToolDefinition{}, false
	}
	return cloneDefinition(entry.definition), true
}

func (catalog *registry) schemas() []llm.ToolSchema {
	return catalog.promptTools().Schemas
}

func (catalog *registry) executionMode(input ToolExecutionInput) ToolExecutionMode {
	arguments, err := jsonvalue.Clone(input.Arguments)
	if err != nil {
		return ExecutionExclusive
	}
	entry, found := catalog.find(input.Name)
	if !found || entry.definition.ConcurrencyBehavior == nil {
		return ExecutionExclusive
	}
	if err := validateSchemaValue(
		entry.parameterSchema,
		arguments,
		"arguments",
	); err != nil {
		return ExecutionExclusive
	}
	if concurrencySafe(entry.definition.ConcurrencyBehavior, arguments) {
		return ExecutionParallel
	}
	return ExecutionExclusive
}

func (catalog *registry) promptTools() systemprompt.ToolProviderResult {
	resolved := catalog.view()
	projections := make([]llm.ToolSchema, 0, len(resolved.order))
	for _, name := range resolved.order {
		entry := resolved.visible[name]
		if entry == nil {
			continue
		}
		projections = append(projections, llm.ToolSchema{
			Name:        entry.definition.Name,
			Description: entry.definition.Description,
			Parameters: append(
				json.RawMessage(nil),
				entry.definition.Parameters...,
			),
		})
	}
	return systemprompt.ToolProviderResult{
		Schemas:    projections,
		KnownNames: sortedNames(resolved.knownNames),
	}
}

func (catalog *registry) view() toolView {
	layers := catalog.toolLayers()
	versions := toolLayerVersions(layers)
	catalog.viewMutex.RLock()
	if sameToolLayerVersions(catalog.viewCache.layers, versions) {
		resolved := catalog.viewCache.view
		catalog.viewMutex.RUnlock()
		return resolved
	}
	catalog.viewMutex.RUnlock()

	resolved := resolveToolView(layers)
	catalog.viewMutex.Lock()
	catalog.viewCache = cachedToolView{
		layers: versions,
		view:   resolved,
	}
	catalog.viewMutex.Unlock()
	return resolved
}

func toolLayerVersions(layers []toolLayerSnapshot) []toolLayerVersion {
	versions := make([]toolLayerVersion, len(layers))
	for layerIndex, layer := range layers {
		versions[layerIndex] = toolLayerVersion{
			source:   layer.source,
			revision: layer.revision,
		}
	}
	return versions
}

func sameToolLayerVersions(
	left []toolLayerVersion,
	right []toolLayerVersion,
) bool {
	if len(left) != len(right) {
		return false
	}
	for layerIndex, leftLayer := range left {
		if leftLayer != right[layerIndex] {
			return false
		}
	}
	return true
}

func (catalog *registry) toolLayers() []toolLayerSnapshot {
	layers := make([]toolLayerSnapshot, 0)
	if catalog.parent != nil {
		layers = append(layers, catalog.parent.toolLayers()...)
	}
	return append(layers, catalog.store.snapshot())
}

func (catalog *registry) guards() []ToolGuard {
	return guardPolicies(catalog.toolLayers())
}

func concurrencySafe(
	classifier ConcurrencyClassifier,
	arguments json.RawMessage,
) (safe bool) {
	defer func() {
		if recover() != nil {
			safe = false
		}
	}()
	return classifier.ConcurrencySafe(
		append(json.RawMessage(nil), arguments...),
	)
}
