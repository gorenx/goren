package extension

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent"
)

// provisioning owns one Activation's installed Extensions across publication
// and residency.
type provisioning struct {
	mutex       sync.Mutex
	effects     []*effect
	invalidated bool
	committed   bool
	closed      bool
}

func (acquired *provisioning) Commit() error {
	acquired.mutex.Lock()
	defer acquired.mutex.Unlock()
	if acquired.closed {
		return errors.New("subagent: Activation Provisioning is closed")
	}
	if acquired.invalidated {
		return &subagent.Error{
			Code: subagent.ErrorActivationExtensionRevoked,
			Message: "a continuable Activation Extension was revoked while " +
				"the child was being built; the child was not established",
		}
	}
	acquired.committed = true
	return nil
}

func (acquired *provisioning) Dispose(closeContext context.Context) error {
	if acquired == nil {
		return nil
	}
	acquired.mutex.Lock()
	if acquired.closed {
		acquired.mutex.Unlock()
		return nil
	}
	acquired.closed = true
	effects := append([]*effect(nil), acquired.effects...)
	acquired.mutex.Unlock()
	return disposeEffects(closeContext, effects)
}

func (acquired *provisioning) invalidate() {
	acquired.mutex.Lock()
	if !acquired.committed {
		acquired.invalidated = true
	}
	acquired.mutex.Unlock()
}

type effect struct {
	once         sync.Once
	owner        *provisioning
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

var _ agent.Provisioning = (*provisioning)(nil)
var _ agent.Effect = (*effect)(nil)
