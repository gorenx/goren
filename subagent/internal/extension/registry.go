// Package extension owns child Extension registrations, per-Scope
// installations, rollback, and installation release.
package extension

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

// Registry owns ordered child Extension registrations and their resident
// installations.
type Registry struct {
	mutex         sync.Mutex
	registrations []*registration
	named         map[string]*registration
}

type registrationState uint8

const (
	registrationActive registrationState = iota
	registrationRemoved
)

type registration struct {
	mutex     sync.Mutex
	extension subagent.Extension
	// name is nil for a common Extension. A non-nil value is the stable
	// Bound-config selection name and does not identify a Tool or Plugin.
	name          *string
	state         registrationState
	installations []*extensionBinding
	closeErr      error
}

// registrationHandle is the caller-owned removal capability. Registry never
// stores it, so the handle may point to Registry without creating a cycle.
type registrationHandle struct {
	owner  *Registry
	record *registration
}

// New constructs an empty Extension Registry.
func New() *Registry {
	return &Registry{
		named: make(map[string]*registration),
	}
}

// RegisterExtension adds one child Extension after every previously
// registered Extension.
func (owner *Registry) RegisterExtension(
	extension subagent.Extension,
	options ...subagent.ExtensionOption,
) (subagent.ExtensionRegistration, error) {
	if extension == nil || nilInterface(extension) {
		return nil, errors.New("subagent: child Extension is required")
	}
	name, err := extensionName(options)
	if err != nil {
		return nil, err
	}
	record := &registration{
		extension: extension,
		name:      name,
	}
	owner.mutex.Lock()
	if name != nil && owner.named[*name] != nil {
		owner.mutex.Unlock()
		return nil, fmt.Errorf(
			"subagent: child Extension name %q is already registered",
			*name,
		)
	}
	owner.registrations = append(owner.registrations, record)
	if name != nil {
		owner.named[*name] = record
	}
	owner.mutex.Unlock()
	return &registrationHandle{
		owner:  owner,
		record: record,
	}, nil
}

func extensionName(options []subagent.ExtensionOption) (*string, error) {
	var selectedName string
	found := false
	for _, option := range options {
		configuredName, configured := option.Name()
		if !configured {
			continue
		}
		if found {
			return nil, errors.New(
				"subagent: child Extension registration has multiple names",
			)
		}
		if strings.TrimSpace(configuredName) == "" ||
			configuredName != strings.TrimSpace(configuredName) {
			return nil, errors.New(
				"subagent: child Extension name must be non-empty and trimmed",
			)
		}
		selectedName = configuredName
		found = true
	}
	if !found {
		return nil, nil
	}
	return &selectedName, nil
}

func (owner *Registry) common() []*registration {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	resolved := make([]*registration, 0, len(owner.registrations))
	for _, record := range owner.registrations {
		if record.name == nil {
			resolved = append(resolved, record)
		}
	}
	return resolved
}

func (owner *Registry) selected(
	extensionNames []string,
) ([]*registration, error) {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	resolved := make([]*registration, 0, len(extensionNames))
	seen := make(map[string]struct{}, len(extensionNames))
	for _, extensionNameValue := range extensionNames {
		if strings.TrimSpace(extensionNameValue) == "" ||
			extensionNameValue != strings.TrimSpace(extensionNameValue) {
			return nil, errors.New(
				"subagent: selected child Extension name must be non-empty and trimmed",
			)
		}
		if _, duplicate := seen[extensionNameValue]; duplicate {
			return nil, fmt.Errorf(
				"subagent: selected child Extension name %q is duplicated",
				extensionNameValue,
			)
		}
		seen[extensionNameValue] = struct{}{}
		record := owner.named[extensionNameValue]
		if record == nil {
			return nil, &subagent.Error{
				Code: subagent.ErrorUnknownExtension,
				Message: fmt.Sprintf(
					"subagent: selected child Extension name %q is not registered",
					extensionNameValue,
				),
			}
		}
		resolved = append(resolved, record)
	}
	return resolved, nil
}

// ValidateSelection verifies a complete ordered named Extension selection
// without installing effects or retaining a Registry snapshot.
func (owner *Registry) ValidateSelection(extensionNames []string) error {
	_, err := owner.selected(extensionNames)
	return err
}

// ListExtensions returns detached descriptors for currently registered named
// Extensions. Registration order does not determine Bound selection order.
func (owner *Registry) ListExtensions() []subagent.ExtensionDescriptor {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	descriptors := make([]subagent.ExtensionDescriptor, 0, len(owner.named))
	for _, record := range owner.registrations {
		if record.name == nil {
			continue
		}
		descriptors = append(descriptors, subagent.ExtensionDescriptor{
			Name: *record.name,
		})
	}
	return descriptors
}

// Clear closes every remaining registration after child close is requested.
func (owner *Registry) Clear(closeContext context.Context) (int, error) {
	owner.mutex.Lock()
	registrations := append([]*registration(nil), owner.registrations...)
	owner.mutex.Unlock()
	var closeErr error
	for index := len(registrations) - 1; index >= 0; index-- {
		closeErr = errors.Join(
			closeErr,
			owner.unregister(closeContext, registrations[index]),
		)
	}
	return len(registrations), closeErr
}

func (handle *registrationHandle) Unregister(closeContext context.Context) error {
	if handle == nil || handle.owner == nil || handle.record == nil {
		return nil
	}
	return handle.owner.unregister(closeContext, handle.record)
}

func (owner *Registry) unregister(
	closeContext context.Context,
	record *registration,
) error {
	record.mutex.Lock()
	if record.state == registrationRemoved {
		closeErr := record.closeErr
		record.mutex.Unlock()
		return closeErr
	}
	record.state = registrationRemoved
	bindings := append([]*extensionBinding(nil), record.installations...)
	record.mutex.Unlock()

	owner.mutex.Lock()
	owner.registrations = slices.DeleteFunc(
		owner.registrations,
		func(candidate *registration) bool {
			return candidate == record
		},
	)
	if record.name != nil && owner.named[*record.name] == record {
		delete(owner.named, *record.name)
	}
	owner.mutex.Unlock()
	closeErr := closeBindings(closeContext, bindings)
	if closeErr != nil {
		closeErr = &subagent.Error{
			Code: subagent.ErrorExtensionReleaseFailed,
			Message: fmt.Sprintf(
				"child Extension removal failed to release %d installation(s)",
				countErrors(closeErr),
			),
			Cause: closeErr,
		}
	}
	record.mutex.Lock()
	record.closeErr = closeErr
	record.mutex.Unlock()
	return closeErr
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

var _ subagent.ExtensionRegistration = (*registrationHandle)(nil)
var _ subagent.ExtensionDirectory = (*Registry)(nil)
