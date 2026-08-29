package commands

import (
	"context"
	"errors"
	"sync"
)

// Registration owns one reversible command definition and the admitted
// handler calls that must quiesce before its plugin can release dependencies.
type Registration struct {
	owner *CommandRuntime
	entry *registeredCommand
	once  sync.Once
}

// Unregister prevents every invocation that has not already been admitted.
func (handle *Registration) Unregister() {
	if handle == nil {
		return
	}
	handle.once.Do(func() {
		handle.owner.unregister(handle.entry)
	})
}

// Wait blocks until every invocation admitted before Unregister has returned.
func (handle *Registration) Wait(waitContext context.Context) error {
	if handle == nil {
		return nil
	}
	if waitContext == nil {
		return errors.New("commands: wait Context is nil")
	}
	return handle.entry.wait(waitContext)
}
