package plugin

import (
	"fmt"
	"reflect"
)

// activationBindings are the exact registry records published by one Fiber.
// Withdrawal is identity-based and idempotent.
type activationBindings struct {
	services       []*serviceBinding
	eventObservers []*eventBinding
	waterfalls     []*waterfallBinding
}

// runtimeBindings owns every dispatch registry and their shared publication
// order. Runtime's view lock guards all methods.
type runtimeBindings struct {
	services   *serviceRegistry
	events     *eventDispatcher
	waterfalls *waterfallRegistry
	nextOrder  uint64
}

func newRuntimeBindings(eventFailures EventFailureReporter) *runtimeBindings {
	return &runtimeBindings{
		services:   newServiceRegistry(),
		events:     newEventDispatcher(eventFailures),
		waterfalls: newWaterfallRegistry(),
	}
}

func (bindings *runtimeBindings) validateAdmission(target pluginTarget) error {
	return bindings.events.validateDeliveryRequirements(target.manifest.events)
}

// validateMountAdmission checks one complete topology batch before any Plugin
// receives Apply. Runtime.view must be read-locked by the caller.
func (bindings *runtimeBindings) validateMountAdmission(
	mounts []*pluginMount,
) error {
	services := make(map[serviceBindingKey]string)
	events := make(map[reflect.Type]eventRef)
	for _, mounted := range mounts {
		if mounted == nil {
			continue
		}
		bindingSpec := mounted.target.manifest
		if err := bindings.validateAdmission(mounted.target); err != nil {
			return err
		}
		for _, serviceDeclaration := range bindingSpec.provides {
			bindingKey := serviceBindingKey{
				scope:       mounted.scope,
				serviceType: serviceDeclaration.reference.key,
			}
			if bindings.services.bindings[bindingKey] != nil {
				return fmt.Errorf(
					"%w: %s",
					ErrServiceConflict,
					serviceDeclaration.reference.name,
				)
			}
			if _, conflict := services[bindingKey]; conflict {
				return fmt.Errorf(
					"%w: %s",
					ErrServiceConflict,
					serviceDeclaration.reference.name,
				)
			}
			services[bindingKey] = serviceDeclaration.reference.name
		}
		for _, eventDeclaration := range bindingSpec.events {
			existingBindings := bindings.events.registry.bindings[eventDeclaration.reference.key]
			if len(existingBindings) != 0 {
				existing := existingBindings[0].reference
				if existing.name != eventDeclaration.reference.name ||
					existing.policy != eventDeclaration.reference.policy {
					return fmt.Errorf(
						"plugin: Event type %q has inconsistent metadata",
						namedTypeName(eventDeclaration.reference.key),
					)
				}
			}
			existing, found := events[eventDeclaration.reference.key]
			if found && (existing.name != eventDeclaration.reference.name ||
				existing.policy != eventDeclaration.reference.policy) {
				return fmt.Errorf(
					"plugin: Event type %q has inconsistent metadata",
					namedTypeName(eventDeclaration.reference.key),
				)
			}
			events[eventDeclaration.reference.key] = eventDeclaration.reference
		}
	}
	return nil
}

func (bindings *runtimeBindings) publish(
	ownerFiber *fiber,
) (activationBindings, error) {
	if err := bindings.validate(ownerFiber); err != nil {
		return activationBindings{}, err
	}
	published := activationBindings{}
	for _, serviceDeclaration := range ownerFiber.target.manifest.provides {
		selectedBinding := &serviceBinding{
			reference:  serviceDeclaration.reference,
			capability: serviceDeclaration.capability,
			owner:      ownerFiber,
			scope:      ownerFiber.scope,
		}
		bindings.services.bindings[serviceBindingKey{
			scope:       ownerFiber.scope,
			serviceType: serviceDeclaration.reference.key,
		}] = selectedBinding
		published.services = append(published.services, selectedBinding)
	}
	for _, eventDeclaration := range ownerFiber.target.manifest.events {
		bindings.nextOrder++
		selectedBinding := &eventBinding{
			reference: eventDeclaration.reference,
			observer:  eventDeclaration.observer,
			owner:     ownerFiber,
			scope:     ownerFiber.scope,
			ordinal:   bindings.nextOrder,
		}
		bindings.events.subscribe(selectedBinding)
		published.eventObservers = append(
			published.eventObservers,
			selectedBinding,
		)
	}
	for _, waterfallDeclaration := range ownerFiber.target.manifest.waterfalls {
		bindings.nextOrder++
		selectedBinding := &waterfallBinding{
			reference: waterfallDeclaration.reference,
			invoker:   waterfallDeclaration.invoker,
			owner:     ownerFiber,
			scope:     ownerFiber.scope,
			ordinal:   bindings.nextOrder,
		}
		bindings.waterfalls.add(selectedBinding)
		published.waterfalls = append(published.waterfalls, selectedBinding)
	}
	return published, nil
}

func (bindings *runtimeBindings) validate(ownerFiber *fiber) error {
	for _, serviceDeclaration := range ownerFiber.target.manifest.provides {
		bindingKey := serviceBindingKey{
			scope:       ownerFiber.scope,
			serviceType: serviceDeclaration.reference.key,
		}
		existingBinding := bindings.services.bindings[bindingKey]
		if existingBinding != nil && existingBinding.owner != ownerFiber {
			return fmt.Errorf(
				"%w: %s",
				ErrServiceConflict,
				serviceDeclaration.reference.name,
			)
		}
	}
	if err := bindings.events.validateMetadata(ownerFiber.target.manifest.events); err != nil {
		return err
	}
	return nil
}

func (bindings *runtimeBindings) withdraw(published activationBindings) {
	for _, selectedBinding := range published.services {
		bindingKey := serviceBindingKey{
			scope:       selectedBinding.scope,
			serviceType: selectedBinding.reference.key,
		}
		if bindings.services.bindings[bindingKey] == selectedBinding {
			delete(bindings.services.bindings, bindingKey)
		}
	}
	for _, selectedBinding := range published.eventObservers {
		bindings.events.unsubscribe(selectedBinding)
	}
	for _, selectedBinding := range published.waterfalls {
		bindings.waterfalls.remove(selectedBinding)
	}
}

func (bindings *runtimeBindings) restore(published activationBindings) {
	for _, selectedBinding := range published.services {
		bindings.services.bindings[serviceBindingKey{
			scope:       selectedBinding.scope,
			serviceType: selectedBinding.reference.key,
		}] = selectedBinding
	}
	for _, selectedBinding := range published.eventObservers {
		bindings.events.subscribe(selectedBinding)
	}
	for _, selectedBinding := range published.waterfalls {
		bindings.waterfalls.add(selectedBinding)
	}
}
