// Package subagents owns the offered Subagent application capabilities.
package subagents

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

// Service is the single Subagent application service published through the
// narrow Starter and ChildControl capability views.
type Service struct {
	mutex       sync.RWMutex
	phase       admissionState
	activeCalls sync.WaitGroup
	control     *childExecutionControl
	// Key is a canonical Subagent mode. Value is its complete implementation
	// for the current open Service cycle.
	implementations map[subagent.Mode]implementation
	closeOrder      []implementation
	closed          chan struct{}
	closeErr        error
}

// New constructs the stable, initially inactive Subagent Service.
func New() *Service {
	return &Service{
		phase:  admissionInactive,
		closed: make(chan struct{}),
	}
}

// Open installs one complete implementation set and begins admitting calls.
// Plugin ownership is deliberately absent from this business contract.
func (owner *Service) Open(
	agentRegistry agentDirectory,
	executionRegistry *sharedexecution.Registry,
	implementations ...implementation,
) error {
	if agentRegistry == nil || executionRegistry == nil ||
		len(implementations) == 0 {
		return errors.New(
			"subagent: Service requires Agent Registry, Execution Registry, " +
				"and implementations",
		)
	}
	// Key is a canonical Subagent mode. Value is its validated implementation.
	implementationIndex := make(
		map[subagent.Mode]implementation,
		len(implementations),
	)
	closeOrder := make([]implementation, 0, len(implementations))
	for _, candidate := range implementations {
		if candidate == nil || candidate.Mode() == "" {
			return errors.New("subagent: implementation is incomplete")
		}
		if validationErr := validateImplementation(candidate); validationErr != nil {
			return validationErr
		}
		if _, duplicate := implementationIndex[candidate.Mode()]; duplicate {
			return fmt.Errorf(
				"subagent: duplicate implementation for mode %q",
				candidate.Mode(),
			)
		}
		implementationIndex[candidate.Mode()] = candidate
		closeOrder = append(closeOrder, candidate)
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.phase == admissionAccepting ||
		owner.phase == admissionClosing {
		return errors.New("subagent: Service is already open")
	}
	owner.activeCalls = sync.WaitGroup{}
	owner.closed = make(chan struct{})
	owner.closeErr = nil
	owner.control = newChildExecutionControl(
		agentRegistry,
		executionRegistry,
		implementationIndex,
	)
	owner.implementations = implementationIndex
	owner.closeOrder = closeOrder
	owner.phase = admissionAccepting
	return nil
}

// Start selects an implementation by the command's stable business mode and
// returns that mode's execution through the public Execution contract.
func (owner *Service) Start(
	ctx context.Context,
	command subagent.StartCommand,
) (subagent.Execution, error) {
	if beginErr := owner.beginCall(); beginErr != nil {
		return nil, beginErr
	}
	defer owner.activeCalls.Done()
	if ctx == nil {
		return nil, errors.New("subagent: Start context is nil")
	}
	if requestErr := ctx.Err(); requestErr != nil {
		return nil, requestErr
	}
	if command == nil {
		return nil, errors.New("subagent: Start command is nil")
	}
	candidate, resolveErr := owner.implementation(command.Mode())
	if resolveErr != nil {
		return nil, resolveErr
	}
	switch typedCommand := command.(type) {
	case subagent.OneShotStartCommand:
		selected, supported := candidate.(oneShot)
		if !supported {
			return nil, unsupportedStart(command.Mode())
		}
		return selected.Start(ctx, typedCommand)
	case subagent.ContinuableStartCommand:
		selected, supported := candidate.(continuable)
		if !supported {
			return nil, unsupportedStart(command.Mode())
		}
		return selected.Start(ctx, typedCommand)
	default:
		return nil, unsupportedStart(command.Mode())
	}
}

// Send admits live child input through the ordinary Agent Inbox and offers
// a cold child to Continuable resume.
func (owner *Service) Send(
	ctx context.Context,
	parentID session.SessionID,
	childSessionID session.SessionID,
	content []agentmessage.ContentBlock,
	options subagent.FollowupOptions,
) (agentmessage.MessageID, error) {
	if beginErr := owner.beginCall(); beginErr != nil {
		return "", beginErr
	}
	defer owner.activeCalls.Done()
	return owner.control.Send(
		ctx,
		parentID,
		childSessionID,
		content,
		options,
	)
}

// Interrupt performs common live-execution lookup and ancestor authorization,
// then delegates only the mode-specific interruption behavior.
func (owner *Service) Interrupt(
	ctx context.Context,
	childSessionID session.SessionID,
	authority subagent.InterruptAuthority,
) error {
	if beginErr := owner.beginCall(); beginErr != nil {
		return beginErr
	}
	defer owner.activeCalls.Done()
	return owner.control.Interrupt(ctx, childSessionID, authority)
}

// Close first closes admission, waits for admitted calls to return, and then
// asks each implementation to converge the executions it owns.
func (owner *Service) Close(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	owner.mutex.Lock()
	if owner.phase == admissionClosed {
		closeErr := owner.closeErr
		owner.mutex.Unlock()
		return closeErr
	}
	if owner.phase == admissionInactive {
		owner.mutex.Unlock()
		return nil
	}
	if owner.phase == admissionClosing {
		closed := owner.closed
		owner.mutex.Unlock()
		select {
		case <-closed:
			owner.mutex.RLock()
			closeErr := owner.closeErr
			owner.mutex.RUnlock()
			return closeErr
		case <-closeContext.Done():
			return closeContext.Err()
		}
	}
	owner.phase = admissionClosing
	closeOrder := append([]implementation(nil), owner.closeOrder...)
	owner.mutex.Unlock()
	owner.activeCalls.Wait()
	var closeErr error
	for index := len(closeOrder) - 1; index >= 0; index-- {
		closeErr = errors.Join(closeErr, closeOrder[index].Close(closeContext))
	}
	owner.mutex.Lock()
	owner.control = nil
	owner.implementations = nil
	owner.closeOrder = nil
	owner.closeErr = closeErr
	owner.phase = admissionClosed
	close(owner.closed)
	owner.mutex.Unlock()
	return closeErr
}

func (owner *Service) beginCall() error {
	owner.mutex.RLock()
	defer owner.mutex.RUnlock()
	if owner.phase != admissionAccepting {
		return unavailable()
	}
	owner.activeCalls.Add(1)
	return nil
}

func (owner *Service) implementation(
	selectedMode subagent.Mode,
) (implementation, error) {
	candidate, found := owner.implementations[selectedMode]
	if found {
		return candidate, nil
	}
	return nil, fmt.Errorf(
		"subagent: no implementation registered for mode %q",
		selectedMode,
	)
}

func unavailable() error {
	return &subagent.Error{
		Code:    subagent.ErrorDraining,
		Message: "Subagents are closing",
	}
}

func unsupportedStart(selectedMode subagent.Mode) error {
	return fmt.Errorf(
		"subagent: implementation for mode %q does not accept its StartCommand",
		selectedMode,
	)
}

func validateImplementation(candidate implementation) error {
	var complete bool
	switch candidate.Mode() {
	case subagent.ModeOneShot:
		_, complete = candidate.(oneShot)
	case subagent.ModeContinuable:
		_, complete = candidate.(continuable)
	default:
		return fmt.Errorf(
			"subagent: unsupported implementation mode %q",
			candidate.Mode(),
		)
	}
	if complete {
		return nil
	}
	return fmt.Errorf(
		"subagent: implementation for mode %q is incomplete",
		candidate.Mode(),
	)
}

var _ subagent.Starter = (*Service)(nil)
var _ subagent.ChildControl = (*Service)(nil)
