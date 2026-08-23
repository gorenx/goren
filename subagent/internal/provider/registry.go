package provider

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/gorenx/goren/subagent"
)

// Events publishes Provider lifecycle facts at the Runtime boundary.
type Events interface {
	Added(context.Context, subagent.Provider) error
	Removed(context.Context, string)
}

// Registry owns exact Provider registrations and their stable order.
type Registry struct {
	mutex       sync.RWMutex
	providers   map[string]*binding
	order       []string
	eventTarget Events
}

type binding struct {
	name     string
	provider subagent.Provider
}

type registration struct {
	mutex    sync.Mutex
	registry *Registry
	record   *binding
	err      error
}

// New constructs an empty Provider registry.
func New(eventTarget Events) *Registry {
	return &Registry{
		providers:   make(map[string]*binding),
		eventTarget: eventTarget,
	}
}

// Register publishes one exact named Provider.
func (registryOwner *Registry) Register(
	requestContext context.Context,
	candidate subagent.Provider,
) (subagent.ProviderRegistration, error) {
	if requestContext == nil {
		return nil, errors.New("subagent: Provider registration context is nil")
	}
	if requestErr := requestContext.Err(); requestErr != nil {
		return nil, requestErr
	}
	registeredName, inspectErr := inspect(candidate)
	if inspectErr != nil {
		return nil, inspectErr
	}
	record := &binding{
		name:     registeredName,
		provider: candidate,
	}
	registryOwner.mutex.Lock()
	if _, exists := registryOwner.providers[registeredName]; exists {
		registryOwner.mutex.Unlock()
		return nil, &subagent.Error{
			Code: subagent.ErrorDuplicateProvider,
			Message: fmt.Sprintf(
				"a subagent provider named %q is already registered",
				registeredName,
			),
		}
	}
	registryOwner.providers[registeredName] = record
	registryOwner.order = append(registryOwner.order, registeredName)
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
	}, nil
}

// Get returns the exact Provider registered under name.
func (registryOwner *Registry) Get(name string) (subagent.Provider, bool) {
	registryOwner.mutex.RLock()
	record, found := registryOwner.providers[name]
	registryOwner.mutex.RUnlock()
	if !found {
		return nil, false
	}
	return record.provider, true
}

// List returns Provider names in successful registration order.
func (registryOwner *Registry) List() []string {
	registryOwner.mutex.RLock()
	names := append([]string(nil), registryOwner.order...)
	registryOwner.mutex.RUnlock()
	return names
}

// Clear removes every process-local registration and returns the prior count.
func (registryOwner *Registry) Clear() int {
	registryOwner.mutex.Lock()
	count := len(registryOwner.providers)
	registryOwner.providers = make(map[string]*binding)
	registryOwner.order = nil
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
	if registrationOwner.registry == nil || registrationOwner.record == nil {
		return registrationOwner.err
	}
	registrationOwner.err = registrationOwner.registry.unregister(
		requestContext,
		registrationOwner.record,
	)
	registrationOwner.registry = nil
	registrationOwner.record = nil
	return registrationOwner.err
}

func (registryOwner *Registry) unregister(
	requestContext context.Context,
	record *binding,
) error {
	registryOwner.mutex.Lock()
	current := registryOwner.providers[record.name]
	if current != record {
		registryOwner.mutex.Unlock()
		return nil
	}
	delete(registryOwner.providers, record.name)
	registryOwner.order = slices.DeleteFunc(
		registryOwner.order,
		func(candidateName string) bool {
			return candidateName == record.name
		},
	)
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
	if registryOwner.providers[record.name] == record {
		delete(registryOwner.providers, record.name)
		registryOwner.order = slices.DeleteFunc(
			registryOwner.order,
			func(candidateName string) bool {
				return candidateName == record.name
			},
		)
	}
	registryOwner.mutex.Unlock()
}

func inspect(candidate subagent.Provider) (
	registeredName string,
	inspectErr error,
) {
	if candidate == nil || nilInterface(candidate) {
		return "", errors.New("subagent: Provider is required")
	}
	defer func() {
		if panicValue := recover(); panicValue != nil {
			registeredName = ""
			inspectErr = fmt.Errorf(
				"subagent: Provider metadata panicked: %v",
				panicValue,
			)
		}
	}()
	registeredName = candidate.Name()
	if strings.TrimSpace(registeredName) == "" ||
		registeredName != strings.TrimSpace(registeredName) {
		return "", errors.New(
			"subagent: Provider name must be non-empty and trimmed",
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

var _ subagent.ProviderRegistration = (*registration)(nil)
