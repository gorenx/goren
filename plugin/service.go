package plugin

import (
	"errors"
	"fmt"
	"strings"
)

type serviceToken struct {
	marker byte
}

type serviceRef struct {
	name  string
	token *serviceToken
}

func (definitionRef serviceRef) validate() error {
	if definitionRef.token == nil || strings.TrimSpace(definitionRef.name) == "" {
		return errors.New("invalid Service definition")
	}
	return nil
}

func (definitionRef serviceRef) sameDefinition(otherRef serviceRef) bool {
	return definitionRef.name == otherRef.name && definitionRef.token == otherRef.token
}

// ServiceDefinition is the sealed definition role accepted by Manifest. Use a
// definition returned by DefineService; implementations outside this package
// cannot forge Runtime identity.
type ServiceDefinition interface {
	Name() string
	serviceReference() serviceRef
}

// TypedServiceDefinition is the owner-defined typed identity of one Service.
// Copy the value exported by its owner package; never recreate it by name.
type TypedServiceDefinition[S Service] struct {
	ref serviceRef
}

// DefineService creates one canonical typed Service definition.
func DefineService[S Service](canonicalName string) TypedServiceDefinition[S] {
	if strings.TrimSpace(canonicalName) == "" || canonicalName != strings.TrimSpace(canonicalName) {
		panic("plugin: service name must be non-empty and trimmed")
	}
	return TypedServiceDefinition[S]{
		ref: serviceRef{
			name:  canonicalName,
			token: &serviceToken{},
		},
	}
}

func (definition TypedServiceDefinition[S]) serviceReference() serviceRef {
	return definition.ref
}

// Name returns the canonical Service name.
func (definition TypedServiceDefinition[S]) Name() string {
	return definition.ref.name
}

// Provide installs a Fiber-owned binding for provider.
func (definition TypedServiceDefinition[S]) Provide(
	pluginContext *Context,
	provider S,
) error {
	if pluginContext == nil {
		return errors.New("plugin: provide through nil Context")
	}
	binding := &serviceBindingOf[S]{
		definition: definition,
		provider:   provider,
	}
	return pluginContext.register(binding)
}

// Require resolves a hard dependency declared by the current Plugin Manifest.
func (definition TypedServiceDefinition[S]) Require(pluginContext *Context) (S, error) {
	var unavailable S
	if pluginContext == nil || pluginContext.ownerFiber == nil {
		return unavailable, ErrContextClosed
	}
	if pluginContext.transaction == nil || pluginContext.transaction.state != mountOpen {
		return unavailable, ErrDependencyResolutionClosed
	}
	if !manifestContains(pluginContext.ownerFiber.manifest.Requires, definition.ref) {
		return unavailable, fmt.Errorf(
			"plugin: %s did not declare required service %q",
			pluginContext.ownerFiber.manifest.Name,
			definition.ref.name,
		)
	}
	dependency, exists := pluginContext.ownerFiber.dependencies[definition.ref.token]
	if !exists || dependency.optional || !dependency.reference.sameDefinition(definition.ref) {
		return unavailable, fmt.Errorf("%w: %s", ErrServiceUnavailable, definition.ref.name)
	}
	typedBinding, matches := dependency.binding.(*serviceBindingOf[S])
	if !matches {
		return unavailable, fmt.Errorf("plugin: service %q has an incompatible definition", definition.ref.name)
	}
	return typedBinding.provider, nil
}

// Resolve reads one optional Service snapshot declared by the current Plugin.
func (definition TypedServiceDefinition[S]) Resolve(pluginContext *Context) (S, bool) {
	var unavailable S
	if pluginContext == nil || pluginContext.ownerFiber == nil ||
		pluginContext.transaction == nil || pluginContext.transaction.state != mountOpen ||
		!manifestContains(pluginContext.ownerFiber.manifest.Optional, definition.ref) {
		return unavailable, false
	}
	dependency, exists := pluginContext.ownerFiber.dependencies[definition.ref.token]
	if !exists || !dependency.optional || !dependency.reference.sameDefinition(definition.ref) {
		return unavailable, false
	}
	typedBinding, matches := dependency.binding.(*serviceBindingOf[S])
	if !matches {
		return unavailable, false
	}
	return typedBinding.provider, true
}

type serviceBindingKey struct {
	scope      ScopeKey
	definition *serviceToken
}

type serviceBinding interface {
	runtimeEntry
	bindingRef() serviceRef
	entryOwner() *fiber
	entryActive() bool
}

type serviceBindingOf[S Service] struct {
	definition TypedServiceDefinition[S]
	provider   S
	owner      *fiberEffect
}

func (binding *serviceBindingOf[S]) bindingRef() serviceRef {
	return binding.definition.ref
}

func (binding *serviceBindingOf[S]) entryOwner() *fiber {
	if binding.owner == nil {
		return nil
	}
	return binding.owner.fiber
}

func (binding *serviceBindingOf[S]) entryActive() bool {
	return binding.owner != nil && binding.owner.state == fiberEffectActive
}

func (binding *serviceBindingOf[S]) Label() string {
	return "provide:" + binding.definition.ref.name
}

func (binding *serviceBindingOf[S]) validateEntry(ownership *fiberEffect) error {
	if ownership.scope != ownership.fiber.rootScope {
		return errors.New("plugin: Service providers must be installed at the Fiber root Scope")
	}
	if !manifestContains(ownership.fiber.manifest.Provides, binding.definition.ref) {
		return fmt.Errorf(
			"plugin: %s did not declare provided service %q",
			ownership.fiber.manifest.Name,
			binding.definition.ref.name,
		)
	}
	registry := ownership.runtime.services
	if existingRef, exists := registry.definitions[binding.definition.ref.name]; exists &&
		!existingRef.sameDefinition(binding.definition.ref) {
		return fmt.Errorf(
			"plugin: service %q was recreated with a different definition",
			binding.definition.ref.name,
		)
	}
	bindingKey := serviceBindingKey{
		scope:      ownership.scope.target,
		definition: binding.definition.ref.token,
	}
	if _, exists := registry.bindings[bindingKey]; exists {
		return fmt.Errorf("%w: %s", ErrServiceConflict, binding.definition.ref.name)
	}
	return nil
}

func (binding *serviceBindingOf[S]) publishEntry(ownership *fiberEffect) {
	registry := ownership.runtime.services
	registry.definitions[binding.definition.ref.name] = binding.definition.ref
	binding.owner = ownership
	registry.bindings[serviceBindingKey{
		scope:      ownership.scope.target,
		definition: binding.definition.ref.token,
	}] = binding
}

func (binding *serviceBindingOf[S]) withdrawEntry(ownership *fiberEffect) {
	registry := ownership.runtime.services
	bindingKey := serviceBindingKey{
		scope:      ownership.scope.target,
		definition: binding.definition.ref.token,
	}
	if currentBinding, exists := registry.bindings[bindingKey]; exists && currentBinding == binding {
		delete(registry.bindings, bindingKey)
	}
	binding.owner = nil
}

func (binding *serviceBindingOf[S]) diagnostic() runtimeEntryDiagnostic {
	return runtimeEntryDiagnostic{
		kind: runtimeEntryService,
		name: binding.definition.ref.name,
	}
}

// serviceRegistry owns canonical definitions and active typed bindings.
// Runtime.state protects its fields; it never calls provider code.
type serviceRegistry struct {
	definitions map[string]serviceRef
	bindings    map[serviceBindingKey]serviceBinding
}

func newServiceRegistry() *serviceRegistry {
	return &serviceRegistry{
		definitions: make(map[string]serviceRef),
		bindings:    make(map[serviceBindingKey]serviceBinding),
	}
}

func (registry *serviceRegistry) resolve(
	definitionRef serviceRef,
	sourceScope *Scope,
) (serviceBinding, bool) {
	if sourceScope == nil {
		return nil, false
	}
	for currentScope := sourceScope; currentScope != nil; currentScope = currentScope.parent {
		bindingKey := serviceBindingKey{
			scope:      currentScope.target,
			definition: definitionRef.token,
		}
		if binding, exists := registry.bindings[bindingKey]; exists {
			ownerFiber := binding.entryOwner()
			if ownerFiber != nil && binding.entryActive() && ownerFiber.state == FiberActive {
				return binding, true
			}
		}
	}
	return nil, false
}

func (registry *serviceRegistry) admitManifest(metadata manifestSpec) error {
	references := make([]serviceRef, 0, len(metadata.Provides)+len(metadata.Requires)+len(metadata.Optional))
	references = append(references, metadata.Provides...)
	references = append(references, metadata.Requires...)
	references = append(references, metadata.Optional...)
	for _, definitionRef := range references {
		if existingRef, exists := registry.definitions[definitionRef.name]; exists &&
			!existingRef.sameDefinition(definitionRef) {
			return fmt.Errorf(
				"plugin: service %q was recreated with a different definition",
				definitionRef.name,
			)
		}
	}
	for _, definitionRef := range references {
		registry.definitions[definitionRef.name] = definitionRef
	}
	return nil
}
