// Package extension owns continuable Activation Extension registrations,
// per-Activation installations, rollback, and immediate resident revocation.
package extension

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"

	"github.com/gorenx/goren/subagent"
)

// Registry owns ordered Activation Extension registrations and their resident
// installations.
type Registry struct {
	mutex         sync.Mutex
	registrations []*registration
}

type registration struct {
	mutex         sync.Mutex
	owner         *Registry
	extension     subagent.ActivationExtension
	removed       bool
	installations []*effect
	closeErr      error
}

// New constructs an empty Extension Registry.
func New() *Registry {
	return &Registry{}
}

// RegisterExtension adds one Extension after every previously registered
// Extension.
func (owner *Registry) RegisterExtension(
	extension subagent.ActivationExtension,
) (subagent.ExtensionRegistration, error) {
	if extension == nil || nilInterface(extension) {
		return nil, errors.New("subagent: Activation Extension is required")
	}
	record := &registration{
		owner:     owner,
		extension: extension,
	}
	owner.mutex.Lock()
	owner.registrations = append(owner.registrations, record)
	owner.mutex.Unlock()
	return record, nil
}

// Clear closes every remaining registration after child close is requested.
func (owner *Registry) Clear(closeContext context.Context) (int, error) {
	owner.mutex.Lock()
	registrations := append([]*registration(nil), owner.registrations...)
	owner.mutex.Unlock()
	var closeErr error
	for _, record := range registrations {
		closeErr = errors.Join(closeErr, record.Unregister(closeContext))
	}
	return len(registrations), closeErr
}

func (record *registration) Unregister(closeContext context.Context) error {
	if record == nil {
		return nil
	}
	record.mutex.Lock()
	if record.removed {
		closeErr := record.closeErr
		record.mutex.Unlock()
		return closeErr
	}
	record.removed = true
	effects := append([]*effect(nil), record.installations...)
	record.mutex.Unlock()

	if record.owner != nil {
		record.owner.mutex.Lock()
		record.owner.registrations = slices.DeleteFunc(
			record.owner.registrations,
			func(candidate *registration) bool {
				return candidate == record
			},
		)
		record.owner.mutex.Unlock()
	}
	closeErr := disposeEffects(closeContext, effects)
	if closeErr != nil {
		closeErr = &subagent.Error{
			Code: subagent.ErrorActivationExtensionReleaseFailed,
			Message: fmt.Sprintf(
				"continuable Extension removal failed to release %d installation(s)",
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

var _ subagent.ExtensionRegistration = (*registration)(nil)
