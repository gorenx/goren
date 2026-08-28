// Package subagents owns the offered Subagent application capabilities.
package subagents

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	boundcontract "github.com/gorenx/goren/subagent/bound"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

// Service is the single Subagent application service published through the
// narrow Starter and ChildControl capability views.
type Service struct {
	mutex       sync.RWMutex
	state       admissionState
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
		state:  admissionInactive,
		closed: make(chan struct{}),
	}
}

// Open installs one complete implementation set and begins admitting calls.
// Plugin ownership is deliberately absent from this business contract.
func (owner *Service) Open(
	agentRegistry agent.Registry,
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
	if owner.state == admissionAccepting ||
		owner.state == admissionClosing {
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
	owner.state = admissionAccepting
	return nil
}

// Start selects an implementation by the command's stable business mode and
// always returns the common Execution contract.
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

// Send delegates Bound admission to the Bound owner so config replacement and
// message admission are serialized. Other resident modes use the ordinary
// Agent Inbox; only an unbound cold child is offered to Continuable resume.
func (owner *Service) Send(
	ctx context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	content []agentmessage.ContentBlock,
	options subagent.FollowupOptions,
) (agentmessage.MessageID, error) {
	if beginErr := owner.beginCall(); beginErr != nil {
		return "", beginErr
	}
	defer owner.activeCalls.Done()
	return owner.control.Send(
		ctx,
		parentAgent,
		childID,
		content,
		options,
	)
}

// List returns the committed global Bound Definition index.
func (owner *Service) List(
	ctx context.Context,
) ([]boundcontract.Definition, error) {
	if beginErr := owner.beginCall(); beginErr != nil {
		return nil, beginErr
	}
	defer owner.activeCalls.Done()
	selected, err := owner.boundImplementation()
	if err != nil {
		return nil, err
	}
	return selected.List(ctx)
}

// Create commits one new global Bound Definition.
func (owner *Service) Create(
	ctx context.Context,
	creation boundcontract.Creation,
) (boundcontract.Definition, error) {
	if beginErr := owner.beginCall(); beginErr != nil {
		return boundcontract.Definition{}, beginErr
	}
	defer owner.activeCalls.Done()
	selected, err := owner.boundImplementation()
	if err != nil {
		return boundcontract.Definition{}, err
	}
	return selected.Create(ctx, creation)
}

// Replace commits one complete next Bound Definition revision.
func (owner *Service) Replace(
	ctx context.Context,
	replacement boundcontract.Replacement,
) (boundcontract.Definition, error) {
	if beginErr := owner.beginCall(); beginErr != nil {
		return boundcontract.Definition{}, beginErr
	}
	defer owner.activeCalls.Done()
	selected, err := owner.boundImplementation()
	if err != nil {
		return boundcontract.Definition{}, err
	}
	return selected.Replace(ctx, replacement)
}

// AgentSessionStarted requests a level-triggered consistency pass. Bound owns
// the task lifetime and returns before Session or Agent I/O begins.
func (owner *Service) AgentSessionStarted(
	subject agent.Agent,
) {
	if beginErr := owner.beginCall(); beginErr != nil {
		return
	}
	defer owner.activeCalls.Done()
	selected, err := owner.boundImplementation()
	if err != nil {
		return
	}
	selected.SessionStarted(subject)
}

// SessionEventAppended forwards the existing post-commit wakeup to Bound
// without performing Session reads in the Plugin observer call stack.
func (owner *Service) SessionEventAppended(fact session.EventAppended) {
	owner.mutex.RLock()
	if owner.state != admissionAccepting {
		owner.mutex.RUnlock()
		return
	}
	owner.activeCalls.Add(1)
	control := owner.control
	owner.mutex.RUnlock()
	defer owner.activeCalls.Done()
	if control != nil && control.bound != nil {
		control.bound.SessionEventAppended(fact)
	}
}

// Interrupt performs common live-execution lookup and ancestor authorization,
// then delegates only the mode-specific interruption behavior.
func (owner *Service) Interrupt(
	ctx context.Context,
	childID session.SessionID,
	authority subagent.InterruptAuthority,
) error {
	if beginErr := owner.beginCall(); beginErr != nil {
		return beginErr
	}
	defer owner.activeCalls.Done()
	return owner.control.Interrupt(ctx, childID, authority)
}

// AgentDisposed completes settlement synchronously when Agent owns the
// structural close transaction. The mode terminator must not dispose the
// already-closing Handle again.
func (owner *Service) AgentDisposed(
	ctx context.Context,
	subject agent.Agent,
) error {
	owner.mutex.RLock()
	control := owner.control
	owner.mutex.RUnlock()
	var disposeErr error
	if control != nil {
		if control.bound != nil {
			disposeErr = control.bound.AgentDisposed(ctx, subject)
		}
		disposeErr = errors.Join(
			disposeErr,
			control.AgentDisposed(ctx, subject),
		)
	}
	return disposeErr
}

// Close first closes admission, waits for admitted calls to return, and then
// asks each implementation to converge the executions it owns.
func (owner *Service) Close(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	owner.mutex.Lock()
	if owner.state == admissionClosed {
		closeErr := owner.closeErr
		owner.mutex.Unlock()
		return closeErr
	}
	if owner.state == admissionInactive {
		owner.mutex.Unlock()
		return nil
	}
	if owner.state == admissionClosing {
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
	owner.state = admissionClosing
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
	owner.state = admissionClosed
	close(owner.closed)
	owner.mutex.Unlock()
	return closeErr
}

func (owner *Service) beginCall() error {
	owner.mutex.RLock()
	defer owner.mutex.RUnlock()
	if owner.state != admissionAccepting {
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

func (owner *Service) boundImplementation() (bound, error) {
	candidate, err := owner.implementation(subagent.ModeBound)
	if err != nil {
		return nil, err
	}
	selected, supported := candidate.(bound)
	if !supported {
		return nil, errors.New(
			"subagent: Bound implementation is incomplete",
		)
	}
	return selected, nil
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
	case subagent.ModeBound:
		_, complete = candidate.(bound)
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
var _ boundcontract.Definitions = (*Service)(nil)
