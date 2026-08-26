// Package extension owns child Extension registrations, per-Scope
// installations, rollback, and immediate resident revocation.
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

// Registry owns ordered child Extension registrations and their resident
// installations.
type Registry struct {
	mutex         sync.Mutex
	registrations []*registration
}

type registrationState uint8

const (
	registrationActive registrationState = iota
	registrationRemoved
)

type registration struct {
	mutex         sync.Mutex
	owner         *Registry
	extension     subagent.Extension
	state         registrationState
	installations []*effect
	closeErr      error
}

// New constructs an empty Extension Registry.
func New() *Registry {
	return &Registry{}
}

// RegisterExtension adds one child Extension after every previously
// registered Extension.
func (owner *Registry) RegisterExtension(
	extension subagent.Extension,
) (subagent.ExtensionRegistration, error) {
	if extension == nil || nilInterface(extension) {
		return nil, errors.New("subagent: child Extension is required")
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
	if record.state == registrationRemoved {
		closeErr := record.closeErr
		record.mutex.Unlock()
		return closeErr
	}
	record.state = registrationRemoved
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

var _ subagent.ExtensionRegistration = (*registration)(nil)
