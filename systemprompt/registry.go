package systemprompt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
)

var (
	variableNamePattern   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	errNilAssembleHandler = errors.New("systemprompt: assemble listener is nil")
	errNilChangeHandler   = errors.New("systemprompt: change listener is nil")
)

type namedRecord[T any] struct {
	name     string
	retained T
	active   bool
}

type namedTable[T any] struct {
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

type namedItem[T any] struct {
	name     string
	retained T
}

type anonymousRecord[T any] struct {
	id       uint64
	retained T
	active   bool
}

type anonymousTable[T any] struct {
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

type promptRegistry struct {
	sourceScope *plugin.Scope
	mu          sync.Mutex
	global      promptLayer
	scoped      map[plugin.ScopeKey]*promptLayer
	nextID      uint64
	toolOrder   []string
}

// ValidateConfig applies defaults and rejects invalid tool ordering before a
// runtime Plugin is constructed.
func ValidateConfig(settings Config) (ValidatedConfig, error) {
	configuredOrder, err := validateToolOrder(settings.ToolOrder)
	if err != nil {
		return ValidatedConfig{}, err
	}
	return ValidatedConfig{
		includeHarnessIdentity: optionEnabled(settings.IncludeHarnessIdentity, true),
		includeRuntimeContext:  optionEnabled(settings.IncludeRuntimeContext, true),
		persona:                settings.Persona,
		toolOrder:              configuredOrder,
	}, nil
}

// New creates one System Prompt registry from prevalidated configuration and
// installs its built-ins as effects owned by sourceScope.
func New(requestContext context.Context, sourceScope *plugin.Scope, settings ValidatedConfig) (SystemPrompt, error) {
	if sourceScope == nil {
		return nil, errors.New("systemprompt: source scope is nil")
	}
	owner := &promptRegistry{sourceScope: sourceScope, scoped: make(map[plugin.ScopeKey]*promptLayer), toolOrder: slices.Clone(settings.toolOrder)}
	if settings.includeHarnessIdentity {
		if _, err := owner.Section(requestContext, sourceScope, PromptSection{
			Name: harnessIdentityName, Order: harnessIdentityOrder, Text: StaticText(harnessIdentityText),
		}); err != nil {
			return nil, err
		}
	}
	if _, err := owner.Section(requestContext, sourceScope, PromptSection{
		Name: PersonaSection, Order: PersonaOrder, Text: StaticText(settings.persona),
	}); err != nil {
		return nil, err
	}
	if !settings.includeRuntimeContext {
		if _, err := owner.SuppressRuntimeContext(requestContext, sourceScope); err != nil {
			return nil, err
		}
	}
	return owner, nil
}

func optionEnabled(selected *bool, fallback bool) bool {
	if selected == nil {
		return fallback
	}
	return *selected
}

// Section registers a named section in ownerScope's exact contribution layer.
func (owner *promptRegistry) Section(requestContext context.Context, ownerScope *plugin.Scope, definition PromptSection) (plugin.Disposer, error) {
	if ownerScope == nil {
		return nil, errors.New("systemprompt: section owner scope is nil")
	}
	if definition.Text == nil {
		return nil, fmt.Errorf("systemprompt: prompt section %q text provider is nil", definition.Name)
	}
	if math.IsNaN(definition.Order) || math.IsInf(definition.Order, 0) {
		return nil, fmt.Errorf("systemprompt: prompt section %q order must be a finite number", definition.Name)
	}
	selectedKey := ownerScope.Target()
	owner.mu.Lock()
	layer := owner.layerLocked(selectedKey)
	record, err := layer.sections.add(definition.Name, definition, owner.duplicateMessage(selectedKey, "section", definition.Name))
	owner.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return owner.ownMutation(requestContext, ownerScope, "systemPrompt.section()", selectedKey, func() {
		layer.sections.remove(record)
	})
}

// Context registers named dynamic runtime context in ownerScope's exact layer.
func (owner *promptRegistry) Context(requestContext context.Context, ownerScope *plugin.Scope, definition PromptContext) (plugin.Disposer, error) {
	if ownerScope == nil {
		return nil, errors.New("systemprompt: context owner scope is nil")
	}
	if definition.Text == nil {
		return nil, fmt.Errorf("systemprompt: prompt context %q text provider is nil", definition.Name)
	}
	if math.IsNaN(definition.Order) || math.IsInf(definition.Order, 0) {
		return nil, fmt.Errorf("systemprompt: prompt context %q order must be a finite number", definition.Name)
	}
	selectedKey := ownerScope.Target()
	owner.mu.Lock()
	layer := owner.layerLocked(selectedKey)
	record, err := layer.contexts.add(definition.Name, definition, owner.duplicateMessage(selectedKey, "context", definition.Name))
	owner.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return owner.ownMutation(requestContext, ownerScope, "systemPrompt.context()", selectedKey, func() {
		layer.contexts.remove(record)
	})
}

// SuppressRuntimeContext hides every registered context in ownerScope's view.
func (owner *promptRegistry) SuppressRuntimeContext(requestContext context.Context, ownerScope *plugin.Scope) (plugin.Disposer, error) {
	if ownerScope == nil {
		return nil, errors.New("systemprompt: suppressor owner scope is nil")
	}
	selectedKey := ownerScope.Target()
	owner.mu.Lock()
	owner.nextID++
	layer := owner.layerLocked(selectedKey)
	record := layer.suppressors.add(owner.nextID, struct{}{})
	owner.mu.Unlock()
	return owner.ownMutation(requestContext, ownerScope, "systemPrompt.suppressRuntimeContext()", selectedKey, func() {
		layer.suppressors.remove(record)
	})
}

// Tools registers one assembly-time tool-schema provider.
func (owner *promptRegistry) Tools(requestContext context.Context, ownerScope *plugin.Scope, callback ToolProvider) (plugin.Disposer, error) {
	if ownerScope == nil {
		return nil, errors.New("systemprompt: tool provider owner scope is nil")
	}
	if callback == nil {
		return nil, errors.New("systemprompt: tool provider is nil")
	}
	selectedKey := ownerScope.Target()
	owner.mu.Lock()
	owner.nextID++
	layer := owner.layerLocked(selectedKey)
	record := layer.providers.add(owner.nextID, callback)
	owner.mu.Unlock()
	return owner.ownMutation(requestContext, ownerScope, "systemPrompt.tools()", selectedKey, func() {
		layer.providers.remove(record)
	})
}

// Variable registers one prompt variable in ownerScope's exact layer.
func (owner *promptRegistry) Variable(requestContext context.Context, ownerScope *plugin.Scope, name string, callback VariableProvider) (plugin.Disposer, error) {
	if ownerScope == nil {
		return nil, errors.New("systemprompt: variable owner scope is nil")
	}
	if !variableNamePattern.MatchString(name) {
		return nil, fmt.Errorf("systemprompt: invalid prompt variable name %q (must match %s)", name, variableNamePattern.String())
	}
	if callback == nil {
		return nil, fmt.Errorf("systemprompt: prompt variable %q provider is nil", name)
	}
	selectedKey := ownerScope.Target()
	owner.mu.Lock()
	layer := owner.layerLocked(selectedKey)
	record, err := layer.variables.add(name, callback, owner.duplicateMessage(selectedKey, "variable", name))
	owner.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return owner.ownMutation(requestContext, ownerScope, "systemPrompt.variable()", selectedKey, func() {
		layer.variables.remove(record)
	})
}

func (owner *promptRegistry) duplicateMessage(selectedKey plugin.ScopeKey, kind string, name string) string {
	if selectedKey.IsGlobal() {
		return fmt.Sprintf("systemprompt: prompt %s %q is already registered (for a per-agent override, register through that agent's scope instead)", kind, name)
	}
	return fmt.Sprintf("systemprompt: prompt %s %q is already registered in this scope", kind, name)
}

func (owner *promptRegistry) layerLocked(selectedKey plugin.ScopeKey) *promptLayer {
	if selectedKey.IsGlobal() {
		return &owner.global
	}
	layer := owner.scoped[selectedKey]
	if layer == nil {
		layer = &promptLayer{}
		owner.scoped[selectedKey] = layer
	}
	return layer
}

func (owner *promptRegistry) ownMutation(requestContext context.Context, ownerScope *plugin.Scope, label string, selectedKey plugin.ScopeKey, undo func()) (plugin.Disposer, error) {
	var initializing atomic.Bool
	initializing.Store(true)
	release, err := plugin.Own(ownerScope, label, func(closeContext context.Context) error {
		owner.mu.Lock()
		undo()
		if !selectedKey.IsGlobal() {
			layer := owner.scoped[selectedKey]
			if layer != nil && layer.empty() {
				delete(owner.scoped, selectedKey)
			}
		}
		owner.mu.Unlock()
		if initializing.Load() {
			return nil
		}
		return plugin.EmitFrom(closeContext, owner.sourceScope, changeEvent, struct{}{})
	})
	if err != nil {
		owner.mu.Lock()
		undo()
		if !selectedKey.IsGlobal() {
			layer := owner.scoped[selectedKey]
			if layer != nil && layer.empty() {
				delete(owner.scoped, selectedKey)
			}
		}
		owner.mu.Unlock()
		return nil, err
	}
	if err := plugin.EmitFrom(requestContext, owner.sourceScope, changeEvent, struct{}{}); err != nil {
		return nil, errors.Join(err, release(requestContext))
	}
	initializing.Store(false)
	return release, nil
}

// Assemble snapshots provider membership, resolves effective contributions,
// applies canonical ordering, and runs the scope-filtered waterfall.
func (owner *promptRegistry) Assemble(requestContext context.Context, assemblyContext AssembleContext) (PromptAssembly, error) {
	state := owner.capture(assemblyContext.Scope)
	variables := make(map[string]VariableValue)
	for _, providerLayer := range state.variableProviders {
		for _, item := range providerLayer {
			resolved, err := item.retained(requestContext, assemblyContext)
			if err != nil {
				return PromptAssembly{}, fmt.Errorf("systemprompt: resolve prompt variable %q: %w", item.name, err)
			}
			variables[item.name] = resolved
		}
	}

	assembledSections := make([]AssembledSection, 0, len(state.sections))
	completeDefinitions := make([]PromptSection, 0, 1)
	for _, definition := range state.sections {
		if definition.Complete {
			completeDefinitions = append(completeDefinitions, definition)
		}
	}
	if len(completeDefinitions) > 1 {
		names := make([]string, len(completeDefinitions))
		for index, definition := range completeDefinitions {
			names[index] = fmt.Sprintf("%q", definition.Name)
		}
		return PromptAssembly{}, fmt.Errorf("systemprompt: multiple complete prompt sections are active: %s", strings.Join(names, ", "))
	}
	var completeSection *AssembledSection
	for _, definition := range state.sections {
		resolvedText, err := definition.Text.ResolveText(requestContext, assemblyContext)
		if err != nil {
			return PromptAssembly{}, fmt.Errorf("systemprompt: resolve prompt section %q: %w", definition.Name, err)
		}
		assembledEntry := AssembledSection{Name: definition.Name, Text: resolvedText}
		assembledSections = append(assembledSections, assembledEntry)
		if definition.Complete {
			retained := assembledEntry
			completeSection = &retained
		}
	}

	assembledContexts := make([]AssembledContext, 0, len(state.contexts))
	if !state.contextSuppressed {
		for _, definition := range state.contexts {
			resolvedText, err := definition.Text.ResolveText(requestContext, assemblyContext)
			if err != nil {
				return PromptAssembly{}, fmt.Errorf("systemprompt: resolve prompt context %q: %w", definition.Name, err)
			}
			assembledContexts = append(assembledContexts, AssembledContext{Name: definition.Name, Text: resolvedText})
		}
	}

	collectedSchemas := make([]llm.ToolSchema, 0)
	knownNames := make(map[string]struct{})
	for _, callback := range state.toolProviders {
		providerResult, err := callback(requestContext, assemblyContext)
		if err != nil {
			return PromptAssembly{}, fmt.Errorf("systemprompt: resolve tool provider: %w", err)
		}
		for _, schema := range providerResult.Schemas {
			detached, detachErr := detachToolSchema(schema)
			if detachErr != nil {
				return PromptAssembly{}, detachErr
			}
			collectedSchemas = append(collectedSchemas, detached)
		}
		acceptedNames := providerResult.KnownNames
		if acceptedNames == nil {
			acceptedNames = make([]string, len(providerResult.Schemas))
			for index, schema := range providerResult.Schemas {
				acceptedNames[index] = schema.Name
			}
		}
		for _, name := range acceptedNames {
			knownNames[name] = struct{}{}
		}
	}
	orderedSchemas, err := orderToolSchemas(collectedSchemas, owner.toolOrder, knownNames)
	if err != nil {
		return PromptAssembly{}, err
	}

	assembled := PromptAssembly{
		Sections: assembledSections, Contexts: assembledContexts, Tools: orderedSchemas, Variables: variables,
	}
	payload := assemblePayload{assembled: &assembled, assemblyContext: assemblyContext}
	transformed, err := plugin.WaterfallScopedFrom(requestContext, owner.sourceScope, assemblyContext.Scope,
		assembleEvent, payload, func(context.Context, assemblePayload) (PromptAssembly, error) {
			return cloneAssembly(assembled), nil
		})
	if err != nil {
		return PromptAssembly{}, err
	}
	if err := validateAssembly(transformed); err != nil {
		return PromptAssembly{}, err
	}
	transformed = cloneAssembly(transformed)
	if completeSection != nil {
		transformed.Sections = []AssembledSection{*completeSection}
	}
	if state.contextSuppressed {
		transformed.Contexts = []AssembledContext{}
	}
	return transformed, nil
}

func (owner *promptRegistry) capture(selectedKey plugin.ScopeKey) promptSnapshot {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	layers := make([]*promptLayer, 0)
	for _, lineageKey := range plugin.ScopeLineage(selectedKey) {
		if layer := owner.scoped[lineageKey]; layer != nil {
			layers = append(layers, layer)
		}
	}
	state := promptSnapshot{
		sections: mergeNamed(owner.global.sections.entries(), layers, func(layer *promptLayer) []namedItem[PromptSection] {
			return layer.sections.entries()
		}),
		contexts: mergeNamed(owner.global.contexts.entries(), layers, func(layer *promptLayer) []namedItem[PromptContext] {
			return layer.contexts.entries()
		}),
		variableProviders: make([][]namedItem[VariableProvider], 0, len(layers)+1),
		toolProviders:     owner.global.providers.values(),
		contextSuppressed: !owner.global.suppressors.empty(),
	}
	state.variableProviders = append(state.variableProviders, owner.global.variables.entries())
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

func mergeNamed[T any](globalEntries []namedItem[T], layers []*promptLayer, pick func(*promptLayer) []namedItem[T]) []T {
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

func validateToolOrder(requested []string) ([]string, error) {
	if requested == nil {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(requested))
	for _, name := range requested {
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("systemprompt: toolOrder lists %q more than once", name)
		}
		seen[name] = struct{}{}
	}
	if _, exists := seen[ToolOrderRest]; !exists {
		return nil, fmt.Errorf("systemprompt: toolOrder must contain the %q rest entry (where unlisted tools are inserted)", ToolOrderRest)
	}
	return slices.Clone(requested), nil
}

func orderToolSchemas(schemas []llm.ToolSchema, requested []string, knownNames map[string]struct{}) ([]llm.ToolSchema, error) {
	for _, schema := range schemas {
		if schema.Name == ToolOrderRest {
			return nil, fmt.Errorf("systemprompt: tool provider returned reserved tool name %q (reserved for toolOrder's rest entry)", ToolOrderRest)
		}
	}
	if requested == nil {
		sort.SliceStable(schemas, func(leftIndex int, rightIndex int) bool {
			return schemas[leftIndex].Name < schemas[rightIndex].Name
		})
		return schemas, nil
	}
	unknown := make([]string, 0)
	listed := make(map[string]struct{}, len(requested))
	for _, name := range requested {
		listed[name] = struct{}{}
		if name != ToolOrderRest {
			if _, exists := knownNames[name]; !exists {
				unknown = append(unknown, name)
			}
		}
	}
	if len(unknown) > 0 {
		known := make([]string, 0, len(knownNames))
		for name := range knownNames {
			known = append(known, name)
		}
		sort.Strings(known)
		quoted := make([]string, len(unknown))
		for index, name := range unknown {
			quoted[index] = fmt.Sprintf("%q", name)
		}
		label := "tool"
		if len(unknown) > 1 {
			label = "tools"
		}
		knownLabel := "(none)"
		if len(known) > 0 {
			knownLabel = strings.Join(known, ", ")
		}
		return nil, fmt.Errorf("systemprompt: toolOrder lists unregistered %s %s; known tools: %s", label, strings.Join(quoted, ", "), knownLabel)
	}
	rest := make([]llm.ToolSchema, 0)
	for _, schema := range schemas {
		if _, exists := listed[schema.Name]; !exists {
			rest = append(rest, schema)
		}
	}
	sort.SliceStable(rest, func(leftIndex int, rightIndex int) bool {
		return rest[leftIndex].Name < rest[rightIndex].Name
	})
	ordered := make([]llm.ToolSchema, 0, len(schemas))
	for _, name := range requested {
		if name == ToolOrderRest {
			ordered = append(ordered, rest...)
			continue
		}
		for _, schema := range schemas {
			if schema.Name == name {
				ordered = append(ordered, schema)
			}
		}
	}
	return ordered, nil
}

func detachToolSchema(schema llm.ToolSchema) (llm.ToolSchema, error) {
	trimmed := strings.TrimSpace(string(schema.Parameters))
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return llm.ToolSchema{}, fmt.Errorf("systemprompt: tool %q parameters must be a JSON object", schema.Name)
	}
	if !jsonValidObject(schema.Parameters) {
		return llm.ToolSchema{}, fmt.Errorf("systemprompt: tool %q parameters must be a valid JSON object", schema.Name)
	}
	return llm.ToolSchema{
		Name: schema.Name, Description: schema.Description, Parameters: slices.Clone(schema.Parameters),
	}, nil
}

func jsonValidObject(raw []byte) bool {
	var decoded map[string]json.RawMessage
	return json.Unmarshal(raw, &decoded) == nil && decoded != nil
}

func validateAssembly(assembled PromptAssembly) error {
	sectionNames := make(map[string]struct{}, len(assembled.Sections))
	for _, entry := range assembled.Sections {
		if entry.Name == "" {
			return errors.New("systemprompt: assembled section names must be non-empty")
		}
		if _, exists := sectionNames[entry.Name]; exists {
			return fmt.Errorf("systemprompt: assembled section name %q is duplicated", entry.Name)
		}
		sectionNames[entry.Name] = struct{}{}
	}
	contextNames := make(map[string]struct{}, len(assembled.Contexts))
	for _, entry := range assembled.Contexts {
		if entry.Name == "" {
			return errors.New("systemprompt: assembled context names must be non-empty")
		}
		if _, exists := contextNames[entry.Name]; exists {
			return fmt.Errorf("systemprompt: assembled context name %q is duplicated", entry.Name)
		}
		contextNames[entry.Name] = struct{}{}
	}
	for _, schema := range assembled.Tools {
		if schema.Name == "" {
			return errors.New("systemprompt: assembled tool names must be non-empty")
		}
	}
	for name := range assembled.Variables {
		if !variableNamePattern.MatchString(name) {
			return fmt.Errorf("systemprompt: assembled variable name %q is invalid", name)
		}
	}
	return nil
}

func cloneAssembly(assembled PromptAssembly) PromptAssembly {
	detached := PromptAssembly{
		Sections:  slices.Clone(assembled.Sections),
		Contexts:  slices.Clone(assembled.Contexts),
		Tools:     make([]llm.ToolSchema, len(assembled.Tools)),
		Variables: make(map[string]VariableValue, len(assembled.Variables)),
	}
	for index, schema := range assembled.Tools {
		detached.Tools[index] = llm.ToolSchema{
			Name: schema.Name, Description: schema.Description, Parameters: slices.Clone(schema.Parameters),
		}
	}
	for name, retained := range assembled.Variables {
		detached.Variables[name] = retained
	}
	return detached
}
