package extension

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent"
)

// setupValidity is the commit-time revocation check shared by bindings made
// during one Extension Setup application.
type setupValidity struct {
	mutex   sync.Mutex
	invalid bool
}

func (validity *setupValidity) Check() error {
	validity.mutex.Lock()
	invalid := validity.invalid
	validity.mutex.Unlock()
	if !invalid {
		return nil
	}
	return &subagent.Error{
		Code: subagent.ErrorExtensionRevoked,
		Message: "a child Extension was revoked while the child was being built; " +
			"the child was not established",
	}
}

func (validity *setupValidity) invalidate() {
	validity.mutex.Lock()
	validity.invalid = true
	validity.mutex.Unlock()
}

// extensionBinding owns one exact Extension resource set. It has no Registry
// or registration back-reference.
type extensionBinding struct {
	once      sync.Once
	resources agent.ScopeResources
	validity  *setupValidity
	err       error
}

func (binding *extensionBinding) Close(closeContext context.Context) error {
	if binding == nil {
		return nil
	}
	binding.once.Do(func() {
		if binding.resources != nil {
			binding.err = binding.resources.Close(closeContext)
		}
	})
	return binding.err
}

func closeBindings(
	closeContext context.Context,
	bindings []*extensionBinding,
) error {
	var closeErr error
	for index := len(bindings) - 1; index >= 0; index-- {
		bindings[index].validity.invalidate()
		closeErr = errors.Join(
			closeErr,
			bindings[index].Close(closeContext),
		)
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

var _ agent.ScopeCheck = (*setupValidity)(nil)
var _ agent.ScopeResource = (*extensionBinding)(nil)
