// Package turnrelay relays completed user Session turns into Bound Inboxes.
package turnrelay

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/session"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

type Diagnostics struct {
	WorkerError func(error)
}

type relayKey struct {
	sessionID session.SessionID
	name      string
}

// Service owns cursor workers independently from Actor subscription.
type Service struct {
	report  func(error)
	inbox   boundcontract.Inbox
	ctx     context.Context
	cancel  context.CancelFunc
	mutex   sync.Mutex
	workers map[relayKey]*worker
	tasks   sync.WaitGroup
}

func New(inbox boundcontract.Inbox, hooks Diagnostics) (*Service, error) {
	if inbox == nil {
		return nil, errors.New("subagent/bound/turnrelay: Inbox is required")
	}
	reporter := hooks.WorkerError
	if reporter == nil {
		reporter = func(error) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		report:  reporter,
		inbox:   inbox,
		ctx:     ctx,
		cancel:  cancel,
		workers: make(map[relayKey]*worker),
	}, nil
}

// Observe reconciles workers from one committed direct-user Session snapshot.
func (owner *Service) Observe(parentID session.SessionID, epoch string, source Source) error {
	if parentID == "" || epoch == "" || source == nil {
		return errors.New("subagent/bound/turnrelay: exact parent source is required")
	}
	snapshot := source.Snapshot()
	bindings, err := sessionBindings(snapshot.Events, parentID)
	if err != nil {
		return fmt.Errorf("subagent/bound/turnrelay: inspect user Session %q: %w", parentID, err)
	}
	owner.mutex.Lock()
	started := make([]*worker, 0, len(bindings))
	for key, current := range owner.workers {
		if key.sessionID == parentID && current.epoch != epoch {
			delete(owner.workers, key)
			current.cancel()
		}
	}
	for _, bindingValue := range bindings {
		key := relayKey{
			sessionID: parentID,
			name:      bindingValue.address.Name,
		}
		if current := owner.workers[key]; current != nil {
			current.wake()
			continue
		}
		workerContext, cancelWorker := context.WithCancel(owner.ctx)
		current := newWorker(owner, workerContext, cancelWorker, epoch, source, owner.inbox, bindingValue)
		owner.workers[key] = current
		owner.tasks.Add(1)
		started = append(started, current)
	}
	owner.mutex.Unlock()
	for _, current := range started {
		go current.run()
	}
	return nil
}

func (owner *Service) StopParent(parentID session.SessionID, epoch string) {
	owner.mutex.Lock()
	for key, current := range owner.workers {
		if key.sessionID == parentID && current.epoch == epoch {
			delete(owner.workers, key)
			current.cancel()
		}
	}
	owner.mutex.Unlock()
}

func (owner *Service) workerClosed(current *worker) {
	key := relayKey{
		sessionID: current.binding.address.SessionID,
		name:      current.binding.address.Name,
	}
	owner.mutex.Lock()
	if owner.workers[key] == current {
		delete(owner.workers, key)
	}
	owner.mutex.Unlock()
	owner.tasks.Done()
}

func (owner *Service) Close(ctx context.Context) error {
	owner.cancel()
	done := make(chan struct{})
	go func() {
		owner.tasks.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}
