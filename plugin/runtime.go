package plugin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
)

type pluginRecord struct {
	id          uint64
	instance    Plugin
	metadata    Manifest
	state       State
	pluginScope *Scope
	lastErr     error
	order       uint64
}

type serviceEntry struct {
	definition ServiceRef
	value      any
	provider   *pluginRecord
	owner      *serviceContribution
}

// Runtime coordinates plugin dependency settlement and owns all active scopes.
// Lifecycle mutations are serialized; service reads and event dispatch may run concurrently.
type Runtime struct {
	operations   sync.Mutex
	mu           sync.RWMutex
	nextID       uint64
	nextOrder    uint64
	nextListener uint64
	records      []*pluginRecord
	providers    map[string]*pluginRecord
	definitions  map[string]ServiceRef
	services     map[string]serviceEntry
	eventDefs    map[string]eventRef
	closed       bool
}

// NewRuntime creates an empty plugin runtime.
func NewRuntime() *Runtime {
	return &Runtime{
		providers:   make(map[string]*pluginRecord),
		definitions: make(map[string]ServiceRef),
		services:    make(map[string]serviceEntry),
		eventDefs:   make(map[string]eventRef),
	}
}

// Load declares a plugin and activates every waiting plugin whose required
// services are now available. Missing dependencies leave the new plugin waiting.
func (engine *Runtime) Load(requestContext context.Context, instance Plugin) (Handle, error) {
	if instance == nil {
		return Handle{}, errors.New("plugin: cannot load nil plugin")
	}
	metadata := instance.Manifest()
	if err := validateManifest(metadata); err != nil {
		return Handle{}, err
	}

	engine.operations.Lock()
	defer engine.operations.Unlock()
	engine.mu.Lock()
	if engine.closed {
		engine.mu.Unlock()
		return Handle{}, errors.New("plugin: runtime is shut down")
	}
	if err := engine.registerDefinitionsLocked(metadata); err != nil {
		engine.mu.Unlock()
		return Handle{}, err
	}
	for _, providedRef := range metadata.Provides {
		if existing := engine.providers[providedRef.name]; existing != nil {
			engine.mu.Unlock()
			return Handle{}, fmt.Errorf("plugin: service %q is already provided by %s", providedRef.name, existing.metadata.Name)
		}
	}
	engine.nextID++
	record := &pluginRecord{id: engine.nextID, instance: instance, metadata: metadata, state: StateWaiting}
	engine.records = append(engine.records, record)
	for _, providedRef := range metadata.Provides {
		engine.providers[providedRef.name] = record
	}
	engine.mu.Unlock()

	reconcileErr := engine.reconcile(requestContext)
	pluginHandle := Handle{owner: engine, id: record.id}
	if record.state == StateFailed {
		return pluginHandle, record.lastErr
	}
	return pluginHandle, reconcileErr
}

// Unload stops active dependents before removing the selected plugin declaration.
// Dependents remain declared and return to waiting until another provider loads.
func (engine *Runtime) Unload(closeContext context.Context, pluginHandle Handle) error {
	engine.operations.Lock()
	defer engine.operations.Unlock()
	record, err := engine.resolveHandle(pluginHandle)
	if err != nil {
		return err
	}
	stopErr := engine.stopDependents(closeContext, record, make(map[uint64]bool))
	stopErr = errors.Join(stopErr, engine.stopRecord(closeContext, record, StateStopped))

	engine.mu.Lock()
	for _, providedRef := range record.metadata.Provides {
		if engine.providers[providedRef.name] == record {
			delete(engine.providers, providedRef.name)
		}
	}
	engine.records = slices.DeleteFunc(engine.records, func(candidate *pluginRecord) bool {
		return candidate == record
	})
	engine.mu.Unlock()
	return stopErr
}

// Replace starts a candidate in a shadow scope, swaps its contributions into
// the existing handle, and only then disposes the former scope. A failed
// candidate leaves the active plugin untouched.
func (engine *Runtime) Replace(requestContext context.Context, pluginHandle Handle, candidate Plugin) error {
	if candidate == nil {
		return errors.New("plugin: replacement is nil")
	}
	candidateMetadata := candidate.Manifest()
	if err := validateManifest(candidateMetadata); err != nil {
		return err
	}

	engine.operations.Lock()
	defer engine.operations.Unlock()
	record, err := engine.resolveHandle(pluginHandle)
	if err != nil {
		return err
	}
	if record.state != StateActive {
		return fmt.Errorf("plugin: %s is %s, not active", record.metadata.Name, record.state)
	}
	if record.metadata.Name != candidateMetadata.Name || !sameRefSet(record.metadata.Provides, candidateMetadata.Provides) {
		return errors.New("plugin: replacement must keep the same name and provided services")
	}
	engine.mu.Lock()
	if err = engine.registerDefinitionsLocked(candidateMetadata); err != nil {
		engine.mu.Unlock()
		return err
	}
	engine.mu.Unlock()
	if missing := engine.missingRequired(candidateMetadata); len(missing) != 0 {
		return fmt.Errorf("plugin: replacement is missing required services: %v", missing)
	}

	shadowRecord := &pluginRecord{
		id:       record.id,
		instance: candidate,
		metadata: candidateMetadata,
		state:    StateStarting,
		order:    record.order,
	}
	shadowScope := newScope(engine, shadowRecord)
	if err = candidate.Apply(requestContext, shadowScope); err != nil {
		shadowRecord.state = StateRollingBack
		return errors.Join(err, shadowScope.dispose(requestContext))
	}
	if err = validateContributions(shadowRecord, shadowScope); err != nil {
		shadowRecord.state = StateRollingBack
		return errors.Join(err, shadowScope.dispose(requestContext))
	}

	dependentsErr := engine.stopDependents(requestContext, record, make(map[uint64]bool))
	oldScope := record.pluginScope
	engine.mu.Lock()
	for serviceName, contribution := range shadowScope.services {
		engine.services[serviceName] = serviceEntry{
			definition: contribution.ref,
			value:      contribution.value,
			provider:   record,
			owner:      contribution,
		}
	}
	record.instance = candidate
	record.metadata = candidateMetadata
	record.pluginScope = shadowScope
	record.lastErr = nil
	record.state = StateActive
	shadowScope.mu.Lock()
	shadowScope.record = record
	shadowScope.activated = true
	shadowScope.mu.Unlock()
	engine.mu.Unlock()

	cleanupErr := oldScope.dispose(requestContext)
	reconcileErr := engine.reconcile(requestContext)
	return errors.Join(dependentsErr, cleanupErr, reconcileErr)
}

// Shutdown unloads every active plugin in dependent-first order and prevents new loads.
func (engine *Runtime) Shutdown(closeContext context.Context) error {
	engine.operations.Lock()
	defer engine.operations.Unlock()
	engine.mu.Lock()
	if engine.closed {
		engine.mu.Unlock()
		return nil
	}
	engine.closed = true
	records := append([]*pluginRecord(nil), engine.records...)
	engine.mu.Unlock()

	var shutdownErr error
	visited := make(map[uint64]bool)
	for index := len(records) - 1; index >= 0; index-- {
		record := records[index]
		shutdownErr = errors.Join(shutdownErr, engine.stopDependents(closeContext, record, visited))
		shutdownErr = errors.Join(shutdownErr, engine.stopRecord(closeContext, record, StateStopped))
		visited[record.id] = true
	}
	engine.mu.Lock()
	clear(engine.services)
	clear(engine.providers)
	engine.mu.Unlock()
	return shutdownErr
}

// Status returns diagnostics for one handle.
func (engine *Runtime) Status(pluginHandle Handle) (PluginStatus, error) {
	record, err := engine.resolveHandle(pluginHandle)
	if err != nil {
		return PluginStatus{}, err
	}
	engine.mu.RLock()
	view := PluginStatus{ID: record.id, Name: record.metadata.Name, State: record.state, Error: record.lastErr}
	pluginScope := record.pluginScope
	engine.mu.RUnlock()
	if pluginScope != nil {
		view.Effects = pluginScope.effectLabels()
	}
	return view, nil
}

// Statuses returns runtime diagnostics in declaration order.
func (engine *Runtime) Statuses() []PluginStatus {
	engine.mu.RLock()
	views := make([]PluginStatus, 0, len(engine.records))
	scopes := make([]*Scope, 0, len(engine.records))
	for _, record := range engine.records {
		views = append(views, PluginStatus{
			ID: record.id, Name: record.metadata.Name, State: record.state, Error: record.lastErr,
		})
		scopes = append(scopes, record.pluginScope)
	}
	engine.mu.RUnlock()
	for index, pluginScope := range scopes {
		if pluginScope != nil {
			views[index].Effects = pluginScope.effectLabels()
		}
	}
	return views
}

func (engine *Runtime) reconcile(requestContext context.Context) error {
	var reconciliationErr error
	for {
		progressed := false
		engine.mu.RLock()
		records := append([]*pluginRecord(nil), engine.records...)
		engine.mu.RUnlock()
		for _, record := range records {
			if record.state != StateWaiting || len(engine.missingRequired(record.metadata)) != 0 {
				continue
			}
			progressed = true
			reconciliationErr = errors.Join(reconciliationErr, engine.activate(requestContext, record))
		}
		if !progressed {
			return reconciliationErr
		}
	}
}

func (engine *Runtime) activate(requestContext context.Context, record *pluginRecord) error {
	engine.mu.Lock()
	record.state = StateStarting
	record.lastErr = nil
	engine.mu.Unlock()
	pluginScope := newScope(engine, record)
	if err := record.instance.Apply(requestContext, pluginScope); err != nil {
		engine.mu.Lock()
		record.state = StateRollingBack
		engine.mu.Unlock()
		rollbackErr := pluginScope.dispose(requestContext)
		engine.mu.Lock()
		record.state = StateFailed
		record.lastErr = errors.Join(err, rollbackErr)
		engine.mu.Unlock()
		return record.lastErr
	}
	if err := validateContributions(record, pluginScope); err != nil {
		engine.mu.Lock()
		record.state = StateRollingBack
		engine.mu.Unlock()
		rollbackErr := pluginScope.dispose(requestContext)
		engine.mu.Lock()
		record.state = StateFailed
		record.lastErr = errors.Join(err, rollbackErr)
		engine.mu.Unlock()
		return record.lastErr
	}

	engine.mu.Lock()
	for serviceName := range pluginScope.services {
		if existing, exists := engine.services[serviceName]; exists && existing.provider != record {
			engine.mu.Unlock()
			rollbackErr := pluginScope.dispose(requestContext)
			engine.mu.Lock()
			record.state = StateFailed
			record.lastErr = errors.Join(fmt.Errorf("plugin: service %q is active", serviceName), rollbackErr)
			engine.mu.Unlock()
			return record.lastErr
		}
	}
	engine.nextOrder++
	record.order = engine.nextOrder
	record.pluginScope = pluginScope
	record.state = StateActive
	pluginScope.mu.Lock()
	pluginScope.activated = true
	pluginScope.mu.Unlock()
	for serviceName, contribution := range pluginScope.services {
		engine.services[serviceName] = serviceEntry{
			definition: contribution.ref,
			value:      contribution.value,
			provider:   record,
			owner:      contribution,
		}
	}
	engine.mu.Unlock()
	return nil
}

func (engine *Runtime) stopDependents(closeContext context.Context, provider *pluginRecord, visited map[uint64]bool) error {
	return engine.stopDependentsFor(closeContext, provider, provider.metadata.Provides, visited)
}

func (engine *Runtime) stopDependentsFor(closeContext context.Context, provider *pluginRecord, providedRefs []ServiceRef, visited map[uint64]bool) error {
	if visited[provider.id] {
		return nil
	}
	visited[provider.id] = true
	engine.mu.RLock()
	records := append([]*pluginRecord(nil), engine.records...)
	engine.mu.RUnlock()
	var stopErr error
	for index := len(records) - 1; index >= 0; index-- {
		dependent := records[index]
		if dependent == provider || dependent.state != StateActive || !dependsOn(dependent.metadata, providedRefs) {
			continue
		}
		stopErr = errors.Join(stopErr, engine.stopDependents(closeContext, dependent, visited))
		stopErr = errors.Join(stopErr, engine.stopRecord(closeContext, dependent, StateWaiting))
	}
	return stopErr
}

func (engine *Runtime) publishService(requestContext context.Context, record *pluginRecord, contribution *serviceContribution) error {
	engine.operations.Lock()
	defer engine.operations.Unlock()
	engine.mu.Lock()
	if engine.closed || record.state != StateActive || engine.providers[contribution.ref.name] != record {
		engine.mu.Unlock()
		return fmt.Errorf("plugin: service %q cannot be provided by an inactive scope", contribution.ref.name)
	}
	if _, exists := engine.services[contribution.ref.name]; exists {
		engine.mu.Unlock()
		return fmt.Errorf("plugin: service %q is already active", contribution.ref.name)
	}
	engine.services[contribution.ref.name] = serviceEntry{
		definition: contribution.ref,
		value:      contribution.value,
		provider:   record,
		owner:      contribution,
	}
	engine.mu.Unlock()
	_ = engine.reconcile(requestContext)
	return nil
}

func (engine *Runtime) withdrawService(closeContext context.Context, record *pluginRecord, definition ServiceRef, contribution *serviceContribution) error {
	engine.operations.Lock()
	defer engine.operations.Unlock()
	engine.mu.RLock()
	entry, exists := engine.services[definition.name]
	engine.mu.RUnlock()
	if !exists || entry.provider != record || entry.owner != contribution {
		return nil
	}
	stopErr := engine.stopDependentsFor(closeContext, record, []ServiceRef{definition}, make(map[uint64]bool))
	engine.mu.Lock()
	entry, exists = engine.services[definition.name]
	if exists && entry.provider == record && entry.owner == contribution {
		delete(engine.services, definition.name)
	}
	engine.mu.Unlock()
	return stopErr
}

func (engine *Runtime) stopRecord(closeContext context.Context, record *pluginRecord, finalState State) error {
	engine.mu.Lock()
	if record.state != StateActive && record.state != StateFailed && record.state != StateWaiting {
		engine.mu.Unlock()
		return nil
	}
	pluginScope := record.pluginScope
	if pluginScope == nil {
		record.state = finalState
		record.lastErr = nil
		engine.mu.Unlock()
		return nil
	}
	record.state = StateStopping
	engine.mu.Unlock()

	cleanupErr := pluginScope.dispose(closeContext)
	engine.mu.Lock()
	for serviceName, entry := range engine.services {
		if entry.provider == record {
			delete(engine.services, serviceName)
		}
	}
	record.pluginScope = nil
	record.state = finalState
	record.lastErr = cleanupErr
	engine.mu.Unlock()
	return cleanupErr
}

func (engine *Runtime) missingRequired(metadata Manifest) []string {
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	missing := make([]string, 0)
	for _, requiredRef := range metadata.Requires {
		entry, exists := engine.services[requiredRef.name]
		if !exists || !entry.definition.sameDefinition(requiredRef) || entry.provider.state != StateActive {
			missing = append(missing, requiredRef.name)
		}
	}
	return missing
}

func (engine *Runtime) resolveService(definition ServiceRef) (any, bool) {
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	entry, exists := engine.services[definition.name]
	if !exists || entry.provider.state != StateActive || !entry.definition.sameDefinition(definition) {
		return nil, false
	}
	return entry.value, true
}

func (engine *Runtime) resolveHandle(pluginHandle Handle) (*pluginRecord, error) {
	if pluginHandle.owner != engine || pluginHandle.id == 0 {
		return nil, errors.New("plugin: handle does not belong to runtime")
	}
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	for _, record := range engine.records {
		if record.id == pluginHandle.id {
			return record, nil
		}
	}
	return nil, errors.New("plugin: handle is no longer loaded")
}

func (engine *Runtime) registerDefinitionsLocked(metadata Manifest) error {
	allRefs := append(append(append([]ServiceRef(nil), metadata.Provides...), metadata.Requires...), metadata.Optional...)
	for _, definition := range allRefs {
		if existing, exists := engine.definitions[definition.name]; exists && !existing.sameDefinition(definition) {
			return fmt.Errorf("plugin: service %q was recreated with a different key or type", definition.name)
		}
		engine.definitions[definition.name] = definition
	}
	return nil
}

func validateContributions(record *pluginRecord, pluginScope *Scope) error {
	pluginScope.mu.Lock()
	defer pluginScope.mu.Unlock()
	for _, providedRef := range record.metadata.Provides {
		contribution := pluginScope.services[providedRef.name]
		if contribution == nil || !contribution.owned {
			return fmt.Errorf("plugin: %s did not provide declared service %q", record.metadata.Name, providedRef.name)
		}
	}
	return nil
}

func sameRefSet(left []ServiceRef, right []ServiceRef) bool {
	if len(left) != len(right) {
		return false
	}
	for _, definition := range left {
		if !containsRef(right, definition) {
			return false
		}
	}
	return true
}

func dependsOn(metadata Manifest, provided []ServiceRef) bool {
	for _, requiredRef := range metadata.Requires {
		if containsRef(provided, requiredRef) {
			return true
		}
	}
	return false
}
