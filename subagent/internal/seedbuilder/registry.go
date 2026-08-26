// Package seedbuilder owns registered child Session seed strategies.
package seedbuilder

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/gorenx/goren/subagent"
)

// Events publishes SeedBuilder registration facts at the Runtime boundary.
type Events interface {
	Added(context.Context, subagent.SeedBuilder) error
	Removed(context.Context, string)
}

// Registry owns exact SeedBuilder registrations and stable lookup order.
type Registry struct {
	mutex       sync.RWMutex
	builders    map[string]*binding
	eventTarget Events
}

type binding struct {
	name    string
	builder subagent.SeedBuilder
}

type registrationState uint8

const (
	registrationRegistered registrationState = iota
	registrationClosed
)

type registration struct {
	mutex    sync.Mutex
	registry *Registry
	record   *binding
	state    registrationState
	err      error
}

// New constructs an empty SeedBuilder Registry.
func New(eventTarget Events) *Registry {
	return &Registry{
		builders:    make(map[string]*binding),
		eventTarget: eventTarget,
	}
}

// Register publishes one exact named SeedBuilder.
func (registryOwner *Registry) Register(
	requestContext context.Context,
	candidate subagent.SeedBuilder,
) (subagent.SeedBuilderRegistration, error) {
	if requestContext == nil {
		return nil, errors.New(
			"subagent: SeedBuilder registration context is nil",
		)
	}
	if requestErr := requestContext.Err(); requestErr != nil {
		return nil, requestErr
	}
	registeredName, inspectErr := inspect(candidate)
	if inspectErr != nil {
		return nil, inspectErr
	}
	record := &binding{
		name:    registeredName,
		builder: candidate,
	}
	registryOwner.mutex.Lock()
	if _, exists := registryOwner.builders[registeredName]; exists {
		registryOwner.mutex.Unlock()
		return nil, &subagent.Error{
			Code: subagent.ErrorDuplicateSeedBuilder,
			Message: fmt.Sprintf(
				"a subagent SeedBuilder named %q is already registered",
				registeredName,
			),
		}
	}
	registryOwner.builders[registeredName] = record
	registryOwner.mutex.Unlock()
	if registryOwner.eventTarget != nil {
		if publishErr := registryOwner.eventTarget.Added(
			requestContext,
			candidate,
		); publishErr != nil {
			registryOwner.rollback(record)
			return nil, publishErr
		}
	}
	return &registration{
		registry: registryOwner,
		record:   record,
		state:    registrationRegistered,
	}, nil
}

// Find returns the exact SeedBuilder registered under name.
func (registryOwner *Registry) Find(
	candidateName string,
) (subagent.SeedBuilder, bool) {
	registryOwner.mutex.RLock()
	record, found := registryOwner.builders[candidateName]
	registryOwner.mutex.RUnlock()
	if !found {
		return nil, false
	}
	return record.builder, true
}

// Clear removes every process-local registration and returns the prior count.
func (registryOwner *Registry) Clear() int {
	registryOwner.mutex.Lock()
	count := len(registryOwner.builders)
	registryOwner.builders = make(map[string]*binding)
	registryOwner.mutex.Unlock()
	return count
}

func (registrationOwner *registration) Unregister(
	requestContext context.Context,
) error {
	if registrationOwner == nil {
		return nil
	}
	registrationOwner.mutex.Lock()
	defer registrationOwner.mutex.Unlock()
	if registrationOwner.state == registrationClosed {
		return registrationOwner.err
	}
	registrationOwner.err = registrationOwner.registry.unregister(
		requestContext,
		registrationOwner.record,
	)
	registrationOwner.state = registrationClosed
	registrationOwner.registry = nil
	registrationOwner.record = nil
	return registrationOwner.err
}

func (registryOwner *Registry) unregister(
	requestContext context.Context,
	record *binding,
) error {
	registryOwner.mutex.Lock()
	current := registryOwner.builders[record.name]
	if current != record {
		registryOwner.mutex.Unlock()
		return nil
	}
	delete(registryOwner.builders, record.name)
	registryOwner.mutex.Unlock()
	if registryOwner.eventTarget != nil {
		if requestContext == nil {
			requestContext = context.Background()
		}
		registryOwner.eventTarget.Removed(
			context.WithoutCancel(requestContext),
			record.name,
		)
	}
	return nil
}

func (registryOwner *Registry) rollback(record *binding) {
	registryOwner.mutex.Lock()
	if registryOwner.builders[record.name] == record {
		delete(registryOwner.builders, record.name)
	}
	registryOwner.mutex.Unlock()
}

func inspect(candidate subagent.SeedBuilder) (
	registeredName string,
	inspectErr error,
) {
	if candidate == nil || nilInterface(candidate) {
		return "", errors.New("subagent: SeedBuilder is required")
	}
	defer func() {
		if panicValue := recover(); panicValue != nil {
			registeredName = ""
			inspectErr = fmt.Errorf(
				"subagent: SeedBuilder metadata panicked: %v",
				panicValue,
			)
		}
	}()
	registeredName = candidate.Name()
	if strings.TrimSpace(registeredName) == "" ||
		registeredName != strings.TrimSpace(registeredName) {
		return "", errors.New(
			"subagent: SeedBuilder name must be non-empty and trimmed",
		)
	}
	return registeredName, nil
}

func nilInterface(candidate any) bool {
	reflected := reflect.ValueOf(candidate)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ subagent.SeedBuilderRegistry = (*Registry)(nil)
var _ subagent.SeedBuilderRegistration = (*registration)(nil)
