package extension

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent"
)

// provisioning owns one child Scope's installed Extensions across publication
// and residency.
type provisioningState uint8

const (
	provisioningPending provisioningState = iota
	provisioningCommitted
	provisioningInvalid
	provisioningClosed
)

type provisioning struct {
	mutex   sync.Mutex
	effects []*effect
	state   provisioningState
}

func (acquired *provisioning) Commit() error {
	acquired.mutex.Lock()
	defer acquired.mutex.Unlock()
	switch acquired.state {
	case provisioningClosed:
		return errors.New("subagent: Extension Provisioning is closed")
	case provisioningInvalid:
		return &subagent.Error{
			Code: subagent.ErrorExtensionRevoked,
			Message: "a Continuable Extension was revoked while " +
				"the child was being built; the child was not established",
		}
	case provisioningCommitted:
		return nil
	case provisioningPending:
		acquired.state = provisioningCommitted
		return nil
	default:
		return errors.New("subagent: Extension Provisioning state is invalid")
	}
}

func (acquired *provisioning) Dispose(closeContext context.Context) error {
	if acquired == nil {
		return nil
	}
	acquired.mutex.Lock()
	if acquired.state == provisioningClosed {
		acquired.mutex.Unlock()
		return nil
	}
	acquired.state = provisioningClosed
	effects := append([]*effect(nil), acquired.effects...)
	acquired.mutex.Unlock()
	return disposeEffects(closeContext, effects)
}

func (acquired *provisioning) invalidate() {
	acquired.mutex.Lock()
	if acquired.state == provisioningPending {
		acquired.state = provisioningInvalid
	}
	acquired.mutex.Unlock()
}

type effect struct {
	once         sync.Once
	owner        *provisioning
	registration *registration
	installation subagent.ExtensionInstallation
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
			removed := installed.registration.state == registrationRemoved
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

var _ agent.Provisioning = (*provisioning)(nil)
var _ agent.ScopeResource = (*effect)(nil)
