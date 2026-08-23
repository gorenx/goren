package composition

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
)

// provisioning owns the prepared lifecycle results of one composed child.
type provisioning struct {
	mutex  sync.Mutex
	parts  []agent.Provisioning
	closed bool
}

func (acquired *provisioning) Commit() error {
	acquired.mutex.Lock()
	defer acquired.mutex.Unlock()
	if acquired.closed {
		return errors.New("subagent: child Provisioning is closed")
	}
	for _, part := range acquired.parts {
		if commitErr := part.Commit(); commitErr != nil {
			return commitErr
		}
	}
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
	parts := append([]agent.Provisioning(nil), acquired.parts...)
	acquired.mutex.Unlock()
	return disposeProvisionings(closeContext, parts)
}

func disposeProvisionings(
	closeContext context.Context,
	parts []agent.Provisioning,
) error {
	var closeErr error
	for index := len(parts) - 1; index >= 0; index-- {
		closeErr = errors.Join(closeErr, parts[index].Dispose(closeContext))
	}
	return closeErr
}

var _ agent.Provisioning = (*provisioning)(nil)
