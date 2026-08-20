package plugin

import (
	"fmt"
	"reflect"
)

type typedServiceType[S Service] struct {
	reference serviceRef
}

// ServiceOf derives one Service identity from its named Go interface type.
// Runtime uses reflection only for this type key; it never invokes business
// methods through reflection.
func ServiceOf[S Service]() ServiceType {
	selectedType := reflect.TypeFor[S]()
	return typedServiceType[S]{
		reference: serviceRef{
			key:  selectedType,
			name: namedTypeName(selectedType),
		},
	}
}

// Name returns the fully qualified Go Service interface name.
func (typedContract typedServiceType[S]) Name() string {
	return typedContract.reference.name
}

func (typedContract typedServiceType[S]) serviceReference() serviceRef {
	return typedContract.reference
}

func (typedServiceType[S]) bindService(pluginInstance Plugin) (Service, bool) {
	capability, matches := pluginInstance.(S)
	if !matches {
		return nil, false
	}
	return capability, true
}

type serviceBindingKey struct {
	scope       *scope
	serviceType reflect.Type
}

type serviceBinding struct {
	reference  serviceRef
	capability Service
	owner      *fiber
	scope      *scope
}

type serviceDependency struct {
	reference serviceRef
	binding   *serviceBinding
	optional  bool
}

type serviceRegistry struct {
	bindings map[serviceBindingKey]*serviceBinding
}

func newServiceRegistry() *serviceRegistry {
	return &serviceRegistry{
		bindings: make(map[serviceBindingKey]*serviceBinding),
	}
}

func (registry *serviceRegistry) resolve(
	reference serviceRef,
	sourceScope *scope,
) (*serviceBinding, bool) {
	for selectedScope := sourceScope; selectedScope != nil; selectedScope = selectedScope.parent {
		bindingKey := serviceBindingKey{
			scope:       selectedScope,
			serviceType: reference.key,
		}
		binding := registry.bindings[bindingKey]
		if binding != nil && binding.owner != nil && binding.owner.state == FiberActive {
			return binding, true
		}
	}
	return nil, false
}

// Require returns the hard Service dependency declared by owner. It is valid
// only while Runtime is invoking owner.Apply.
func Require[S Service](owner Plugin) (S, error) {
	var unavailable S
	ownerFiber, err := fiberOf(owner)
	if err != nil {
		return unavailable, err
	}
	reference := ServiceOf[S]().serviceReference()
	runtimeEngine := ownerFiber.runtime
	runtimeEngine.view.RLock()
	defer runtimeEngine.view.RUnlock()
	if ownerFiber == nil || ownerFiber.state != FiberStarting {
		return unavailable, ErrDependencyResolutionClosed
	}
	if !containsService(ownerFiber.target.manifest.requires, reference.key) {
		return unavailable, fmt.Errorf(
			"plugin: %s did not declare required Service %q",
			ownerFiber.target.manifest.name,
			reference.name,
		)
	}
	dependency := ownerFiber.dependencies[reference.key]
	if dependency == nil || dependency.optional || dependency.binding == nil {
		return unavailable, fmt.Errorf(
			"%w: %s",
			ErrServiceUnavailable,
			reference.name,
		)
	}
	capability, matches := dependency.binding.capability.(S)
	if !matches {
		return unavailable, fmt.Errorf(
			"plugin: Service %q has an incompatible provider",
			reference.name,
		)
	}
	return capability, nil
}

// Resolve returns the optional Service snapshot declared by owner. It is valid
// only while Runtime is invoking owner.Apply.
func Resolve[S Service](owner Plugin) (S, bool) {
	var unavailable S
	ownerFiber, err := fiberOf(owner)
	if err != nil {
		return unavailable, false
	}
	reference := ServiceOf[S]().serviceReference()
	runtimeEngine := ownerFiber.runtime
	runtimeEngine.view.RLock()
	defer runtimeEngine.view.RUnlock()
	if ownerFiber == nil || ownerFiber.state != FiberStarting ||
		!containsService(ownerFiber.target.manifest.optional, reference.key) {
		return unavailable, false
	}
	dependency := ownerFiber.dependencies[reference.key]
	if dependency == nil || !dependency.optional || dependency.binding == nil {
		return unavailable, false
	}
	capability, matches := dependency.binding.capability.(S)
	if !matches {
		return unavailable, false
	}
	return capability, true
}
