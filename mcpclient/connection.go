package mcpclient

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/userquestions"
)

// Connection owns one configured MCP target and its replaceable transports.
// Calls capture a transport at admission; reconnecting never replays a call.
type Connection struct {
	providerID tools.ProviderID
	settings   Config
	questions  userquestions.UserQuestions
	mutex      sync.Mutex
	current    *transport
	// retired retains transports whose release has not been confirmed.
	retired  []*transport
	closed   bool
	paused   bool
	lifetime context.Context
	cancel   context.CancelFunc
	// operations serializes discovery and lifecycle effects without holding mutex.
	operations chan struct{}
}

// Open validates a configured target and opens its first complete transport.
// The caller owns Close independently of the startup Context's later lifetime.
func Open(ctx context.Context, settings Config, questions userquestions.UserQuestions) (*Connection, error) {
	peer, err := openTransport(ctx, settings, questions)
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	return &Connection{
		providerID: tools.ProviderID(uuid.NewString()),
		settings:   peer.settings,
		questions:  questions,
		current:    peer,
		lifetime:   lifetime,
		cancel:     cancel,
		operations: make(chan struct{}, 1),
	}, nil
}

// Changes returns the selected transport's coalesced discovery notifications.
// A caller must obtain the channel again after a successful Reconnect.
func (owner *Connection) Changes() <-chan struct{} {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	return owner.current.Changes()
}

// Done identifies the selected transport lifetime, not permanent Connection closure.
// Reconnect selects a fresh transport with its own completion channel.
func (owner *Connection) Done() <-chan struct{} {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	return owner.current.Done()
}

// Cause reports the selected transport's failure without remote data.
func (owner *Connection) Cause() error {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	return owner.current.Cause()
}

// Schemas returns detached definitions from the selected transport.
func (owner *Connection) Schemas() []llm.ToolSchema {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	return owner.current.Schemas()
}

// Refresh discovers tools on the current transport without replacing it.
func (owner *Connection) Refresh(ctx context.Context) error {
	if err := owner.checkAdmission(); err != nil {
		return err
	}
	work, finish, err := owner.operation(ctx)
	if err != nil {
		return err
	}
	defer finish()
	owner.mutex.Lock()
	peer := owner.current
	paused := owner.paused
	owner.mutex.Unlock()
	if paused {
		return ErrDraining
	}
	return peer.Refresh(work)
}

// Reconnect opens the same configured target and selects it for future calls.
// Provider identity is stable. Old calls retain their transport; cleanup failures
// retain retired transports and do not roll back the successfully selected one.
func (owner *Connection) Reconnect(ctx context.Context) error {
	if err := owner.checkAdmission(); err != nil {
		return err
	}
	work, finish, err := owner.operation(ctx)
	if err != nil {
		return err
	}
	defer finish()
	owner.mutex.Lock()
	paused := owner.paused
	owner.mutex.Unlock()
	if paused {
		return ErrDraining
	}
	candidate, err := openTransport(work, owner.settings, owner.questions)
	if err != nil {
		return err
	}
	owner.mutex.Lock()
	if owner.closed || work.Err() != nil {
		owner.retired = append(owner.retired, candidate)
		owner.mutex.Unlock()
		failure := candidate.Close(context.WithoutCancel(ctx))
		if failure == nil {
			owner.mutex.Lock()
			owner.retired = owner.retired[:len(owner.retired)-1]
			owner.mutex.Unlock()
		}
		return errors.Join(errors.New("mcpclient: connection closed during reconnect"), failure)
	}
	owner.retired = append(owner.retired, owner.current)
	owner.current = candidate
	owner.mutex.Unlock()
	return owner.releaseRetired(work, true)
}

func (owner *Connection) checkAdmission() error {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.closed {
		return errors.New("mcpclient: connection is unavailable")
	}
	if owner.paused {
		return ErrDraining
	}
	return nil
}

// Close freezes admission, cancels connection attempts and releases every transport.
// A timeout retains release responsibility for a later Close call.
func (owner *Connection) Close(ctx context.Context) error {
	if ctx == nil {
		return errors.New("mcpclient: close context is required")
	}
	owner.mutex.Lock()
	owner.closed = true
	owner.cancel()
	peer := owner.current
	retired := append([]*transport(nil), owner.retired...)
	owner.mutex.Unlock()
	// Closing current I/O also unblocks discovery before waiting for its operation.
	failure := peer.Close(ctx)
	for _, previous := range retired {
		failure = errors.Join(failure, previous.Close(ctx))
	}
	select {
	case owner.operations <- struct{}{}:
		defer func() { <-owner.operations }()
	case <-ctx.Done():
		return errors.Join(failure, ctx.Err())
	}
	return errors.Join(failure, owner.releaseRetired(ctx, false))
}

func (owner *Connection) operation(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, errors.New("mcpclient: operation context is required")
	}
	work, cancel := context.WithCancel(ctx)
	if owner.lifetime.Err() != nil {
		cancel()
		return nil, nil, errors.New("mcpclient: connection is unavailable")
	}
	stop := context.AfterFunc(owner.lifetime, cancel)
	select {
	case owner.operations <- struct{}{}:
		if work.Err() == nil {
			return work, func() {
				<-owner.operations
				stop()
				cancel()
			}, nil
		}
		<-owner.operations
	case <-work.Done():
	}
	stop()
	cancel()
	return nil, nil, work.Err()
}

func (owner *Connection) releaseRetired(ctx context.Context, drain bool) error {
	var failures []error
	for index := len(owner.retired) - 1; index >= 0; index-- {
		peer := owner.retired[index]
		if drain && peer.lifetime.Err() == nil {
			if err := peer.Drain(ctx); err != nil {
				failures = append(failures, err)
				continue
			}
		}
		if err := peer.Close(ctx); err != nil {
			failures = append(failures, err)
			continue
		}
		owner.mutex.Lock()
		owner.retired = append(owner.retired[:index], owner.retired[index+1:]...)
		owner.mutex.Unlock()
	}
	return errors.Join(failures...)
}
