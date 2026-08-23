// Package setup owns continuable Setup registrations, per-Activation
// installations, rollback, and immediate resident revocation.
package setup

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

// Input is the immutable identity known before an Agent Scope exists.
type Input struct {
	ChildID    session.SessionID
	ParentID   session.SessionID
	Descriptor subagent.ContinuableDescriptor
}

// Registry owns ordered Setup registrations and their resident installations.
type Registry struct {
	mutex         sync.Mutex
	registrations []*registration
}

type registration struct {
	mutex         sync.Mutex
	owner         *Registry
	contribution  subagent.Setup
	removed       bool
	installations []*effect
	closeErr      error
}

// New constructs an empty Setup Registry.
func New() *Registry {
	return &Registry{}
}

// Register adds one contribution after every previously registered Setup.
func (owner *Registry) Register(
	contribution subagent.Setup,
) (subagent.SetupRegistration, error) {
	if contribution == nil || nilInterface(contribution) {
		return nil, errors.New("subagent: Setup contribution is required")
	}
	record := &registration{
		owner:        owner,
		contribution: contribution,
	}
	owner.mutex.Lock()
	owner.registrations = append(owner.registrations, record)
	owner.mutex.Unlock()
	return record, nil
}

// Compose creates one fresh Agent Setup from the current registration view.
func (owner *Registry) Compose(setupInput Input) agent.Setup {
	return &activationSetup{
		registry: owner,
		input:    setupInput,
	}
}

// Clear closes every remaining registration after child Activations drain.
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
			Code: subagent.ErrorActivationSetupReleaseFailed,
			Message: fmt.Sprintf(
				"continuable Setup removal failed to release %d installation(s)",
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

type activationSetup struct {
	mutex       sync.Mutex
	registry    *Registry
	input       Input
	effects     []*effect
	invalidated bool
	committed   bool
	closed      bool
}

func (setup *activationSetup) Prepare(
	requestContext context.Context,
	scope agent.Scope,
) error {
	if setup == nil || setup.registry == nil || scope == nil {
		return errors.New("subagent: Activation Setup is unavailable")
	}
	setup.registry.mutex.Lock()
	registrations := append(
		[]*registration(nil),
		setup.registry.registrations...,
	)
	setup.registry.mutex.Unlock()
	activation := subagent.ActivationContext{
		ChildID:    setup.input.ChildID,
		ParentID:   setup.input.ParentID,
		Agent:      scope.Agent(),
		Descriptor: setup.input.Descriptor,
	}
	for _, record := range registrations {
		record.mutex.Lock()
		removed := record.removed
		record.mutex.Unlock()
		if removed {
			continue
		}
		installation, installErr := record.contribution.Install(
			requestContext,
			activation,
		)
		if installErr != nil {
			return installErr
		}
		if installation == nil || nilInterface(installation) {
			return errors.New("subagent: Setup returned a nil Installation")
		}
		installed := &effect{
			owner:        setup,
			registration: record,
			installation: installation,
		}
		record.mutex.Lock()
		record.installations = append(record.installations, installed)
		removed = record.removed
		record.mutex.Unlock()
		setup.mutex.Lock()
		setup.effects = append(setup.effects, installed)
		setup.mutex.Unlock()
		if removed {
			setup.invalidate()
			if disposeErr := installed.Dispose(
				context.WithoutCancel(requestContext),
			); disposeErr != nil {
				return disposeErr
			}
		}
	}
	return nil
}

func (setup *activationSetup) Commit() error {
	setup.mutex.Lock()
	defer setup.mutex.Unlock()
	if setup.invalidated {
		return &subagent.Error{
			Code: subagent.ErrorActivationSetupRevoked,
			Message: "a continuable Setup was revoked while the child " +
				"was being built; the child was not established",
		}
	}
	setup.committed = true
	return nil
}

func (setup *activationSetup) Dispose(closeContext context.Context) error {
	if setup == nil {
		return nil
	}
	setup.mutex.Lock()
	if setup.closed {
		setup.mutex.Unlock()
		return nil
	}
	setup.closed = true
	effects := append([]*effect(nil), setup.effects...)
	setup.mutex.Unlock()
	return disposeEffects(closeContext, effects)
}

func (setup *activationSetup) invalidate() {
	setup.mutex.Lock()
	if !setup.committed {
		setup.invalidated = true
	}
	setup.mutex.Unlock()
}

type effect struct {
	once         sync.Once
	owner        *activationSetup
	registration *registration
	installation subagent.Installation
	err          error
}

func (installed *effect) Dispose(closeContext context.Context) error {
	if installed == nil {
		return nil
	}
	installed.once.Do(func() {
		if installed.registration != nil {
			installed.registration.mutex.Lock()
			installed.registration.installations = slices.DeleteFunc(
				installed.registration.installations,
				func(candidate *effect) bool {
					return candidate == installed
				},
			)
			removed := installed.registration.removed
			installed.registration.mutex.Unlock()
			if removed && installed.owner != nil {
				installed.owner.invalidate()
			}
		}
		if closeContext == nil {
			closeContext = context.Background()
		}
		installed.err = installed.installation.Uninstall(
			context.WithoutCancel(closeContext),
		)
	})
	return installed.err
}

func disposeEffects(closeContext context.Context, effects []*effect) error {
	var closeErr error
	for _, installed := range effects {
		closeErr = errors.Join(closeErr, installed.Dispose(closeContext))
	}
	return closeErr
}

func countErrors(joined error) int {
	if joined == nil {
		return 0
	}
	type unwrapper interface {
		Unwrap() []error
	}
	if many, found := joined.(unwrapper); found {
		return len(many.Unwrap())
	}
	return 1
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

var _ subagent.SetupRegistration = (*registration)(nil)
var _ agent.Setup = (*activationSetup)(nil)
var _ agent.Effect = (*effect)(nil)
