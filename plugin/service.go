package plugin

import (
	"errors"
	"fmt"
	"strings"
)

type serviceToken struct {
	marker byte
}

// ServiceKey is an owner-defined typed identity for one service contract.
// Copy the value exported by the owner package; do not recreate it by name.
type ServiceKey[T any] struct {
	ref ServiceRef
}

// Ref returns the type-erased identity used in a Manifest.
func (typedKey ServiceKey[T]) Ref() ServiceRef {
	return typedKey.ref
}

// ServiceRef is the type-erased service identity used by plugin manifests.
type ServiceRef struct {
	name  string
	token *serviceToken
}

// DefineService creates the canonical key owned by a service-definition package.
func DefineService[T any](canonicalName string) ServiceKey[T] {
	if strings.TrimSpace(canonicalName) == "" || canonicalName != strings.TrimSpace(canonicalName) {
		panic("plugin: service name must be non-empty and trimmed")
	}
	return ServiceKey[T]{
		ref: ServiceRef{
			name:  canonicalName,
			token: &serviceToken{},
		},
	}
}

// Name returns the canonical source-compatible service name.
func (definition ServiceRef) Name() string {
	return definition.name
}

func (definition ServiceRef) validate() error {
	if definition.token == nil || strings.TrimSpace(definition.name) == "" {
		return errors.New("invalid service key")
	}
	return nil
}

func (definition ServiceRef) sameDefinition(otherRef ServiceRef) bool {
	return definition.name == otherRef.name && definition.token == otherRef.token
}

// Provide contributes an implementation from the owning plugin scope. The
// service becomes visible only after Apply succeeds and Runtime activates the scope.
func Provide[T any](pluginScope *Scope, providedKey ServiceKey[T], instance T) (Disposer, error) {
	if pluginScope == nil {
		return nil, errors.New("plugin: provide on nil scope")
	}
	return pluginScope.provide(providedKey.ref, instance)
}

// Require resolves a required or optional service declared by the plugin.
func Require[T any](pluginScope *Scope, dependencyKey ServiceKey[T]) (T, bool) {
	var zero T
	if pluginScope == nil {
		return zero, false
	}
	value, found := pluginScope.require(dependencyKey.ref)
	if !found {
		return zero, false
	}
	typedValue, ok := value.(T)
	if !ok {
		panic(fmt.Sprintf("plugin: service %q contains incompatible value %T", dependencyKey.ref.name, value))
	}
	return typedValue, true
}
