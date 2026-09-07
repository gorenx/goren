package tools

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/llm"
)

// registry owns one exact layer of Tool definitions and policies. It has no
// framework lifecycle and never executes a Tool; ToolLayer publishes its mutations
// and Runtime consumes its read view.
type registry struct {
	root  bool
	store *toolStore
	base  []toolLayerSnapshot
}

func newRegistry(root bool) *registry {
	return &registry{
		root:  root,
		store: newToolStore(),
	}
}

func (catalog *registry) setBase(layers []toolLayerSnapshot) error {
	if catalog.root {
		return errors.New("tools: root Registry cannot inherit a base")
	}
	if len(layers) == 0 {
		return errors.New("tools: child Registry requires a base snapshot")
	}
	catalog.base = append([]toolLayerSnapshot(nil), layers...)
	return nil
}

func (catalog *registry) clear() {
	catalog.base = nil
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

func (catalog *registry) replaceToolSet(
	previous []*registeredTool,
	candidates []*registeredTool,
) (toolSetMutation, error) {
	return catalog.store.replaceToolSet(previous, candidates)
}

func (catalog *registry) rollbackToolSet(mutation toolSetMutation) {
	catalog.store.rollbackToolSet(mutation)
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
	return catalog.definitions().Schemas
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

func (catalog *registry) definitions() Definitions {
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
	return Definitions{
		Schemas:    projections,
		KnownNames: sortedNames(resolved.knownNames),
	}
}

func (catalog *registry) view() toolView {
	return resolveToolView(catalog.toolLayers())
}

func (catalog *registry) toolLayers() []toolLayerSnapshot {
	layers := make([]toolLayerSnapshot, 0, len(catalog.base)+1)
	layers = append(layers, catalog.base...)
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
