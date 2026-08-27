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
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

// Service is the single Subagent application service published through the
// narrow Starter and ChildControl capability views.
type Service struct {
	mutex           sync.RWMutex
	state           admissionState
	activeCalls     sync.WaitGroup
	agents          agent.Registry
	executions      *sharedexecution.Registry
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
	implementationIndex := make(map[subagent.Mode]implementation, len(implementations))
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
	owner.agents = agentRegistry
	owner.executions = executionRegistry
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
	case subagent.BoundStartCommand:
		selected, supported := candidate.(bound)
		if !supported {
			return nil, unsupportedStart(command.Mode())
		}
		return selected.Start(ctx, typedCommand)
	default:
		return nil, unsupportedStart(command.Mode())
	}
}

// Send delivers to a resident child through the ordinary Agent interface. If
// the child is not resident, only the Continuable lifecycle strategy may
// materialize it and atomically accept the first message of the new epoch.
func (owner *Service) Send(
	requestContext context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	content []agentmessage.ContentBlock,
	options subagent.FollowupOptions,
) (agentmessage.MessageID, error) {
	if beginErr := owner.beginCall(); beginErr != nil {
		return "", beginErr
	}
	defer owner.activeCalls.Done()
	if requestContext == nil {
		return "", errors.New("subagent: Send context is nil")
	}
	if requestErr := requestContext.Err(); requestErr != nil {
		return "", requestErr
	}
	messageValue, messageErr := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: content,
		Source:  options.Source,
	})
	if messageErr != nil {
		return "", messageErr
	}
	entry, found := owner.executions.Find(childID)
	if found {
		if authorizationErr := owner.authorizeParent(
			entry,
			parentAgent,
		); authorizationErr != nil {
			return "", authorizationErr
		}
		if submitErr := entry.Subject.Followup(messageValue); submitErr != nil {
			return "", submitErr
		}
		return messageValue.StableID(), nil
	}
	candidate, resolveErr := owner.implementation(subagent.ModeContinuable)
	if resolveErr != nil {
		return "", resolveErr
	}
	selected, supported := candidate.(continuable)
	if !supported {
		return "", errors.New(
			"subagent: Continuable implementation does not support resume",
		)
	}
	return selected.Resume(
		requestContext,
		parentAgent,
		childID,
		messageValue,
	)
}

// Interrupt performs common live-execution lookup and ancestor authorization,
// then delegates only the mode-specific interruption behavior.
func (owner *Service) Interrupt(
	requestContext context.Context,
	childID session.SessionID,
	authority subagent.InterruptAuthority,
) error {
	if beginErr := owner.beginCall(); beginErr != nil {
		return beginErr
	}
	defer owner.activeCalls.Done()
	entry, found := owner.executions.Find(childID)
	if !found {
		return nil
	}
	if authorizationErr := authorizeInterrupt(
		owner.agents,
		entry,
		authority,
	); authorizationErr != nil {
		return authorizationErr
	}
	candidate, resolveErr := owner.implementation(entry.Mode)
	if resolveErr != nil {
		return resolveErr
	}
	return candidate.Interrupt(requestContext, childID)
}

// AgentDisposed completes settlement synchronously when Agent owns the
// structural close transaction. The mode terminator must not dispose the
// already-closing Handle again.
func (owner *Service) AgentDisposed(
	requestContext context.Context,
	subject agent.Agent,
) error {
	if subject == nil {
		return nil
	}
	owner.mutex.RLock()
	executionRegistry := owner.executions
	owner.mutex.RUnlock()
	if executionRegistry == nil {
		return nil
	}
	entry, found := executionRegistry.Find(subject.ID())
	if !found || !agent.Same(entry.Subject, subject) {
		return nil
	}
	return entry.Execution.StopAndWait(
		requestContext,
		sharedexecution.StopExternal,
	)
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
	owner.agents = nil
	owner.executions = nil
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

func (owner *Service) authorizeParent(
	entry sharedexecution.Entry,
	parentAgent agent.Agent,
) error {
	if parentAgent != nil && owner.agents.Contains(parentAgent) &&
		agent.Same(entry.Parent, parentAgent) {
		return nil
	}
	return &subagent.Error{
		Code: subagent.ErrorUnauthorized,
		Message: fmt.Sprintf(
			"subagent %q delivery requires its exact live parent Agent",
			entry.Subject.ID(),
		),
	}
}

func authorizeInterrupt(
	agentRegistry agent.Registry,
	entry sharedexecution.Entry,
	authority subagent.InterruptAuthority,
) error {
	switch evidence := authority.(type) {
	case subagent.UserInterruptAuthority:
		if entry.Parent.ID() == evidence.ParentSessionID {
			return nil
		}
	case subagent.AncestorInterruptAuthority:
		if evidence.Agent != nil && agentRegistry.Contains(evidence.Agent) &&
			isLiveAncestor(agentRegistry, entry.Subject, evidence.Agent) {
			return nil
		}
	}
	return &subagent.Error{
		Code: subagent.ErrorUnauthorized,
		Message: fmt.Sprintf(
			"interrupting subagent %q is not authorized",
			entry.Subject.ID(),
		),
	}
}

func isLiveAncestor(
	agentRegistry agent.Registry,
	childAgent agent.Agent,
	candidate agent.Agent,
) bool {
	seen := make(map[session.SessionID]struct{})
	parentID := childAgent.SessionValue().Header().ParentSession
	for parentID != nil {
		if _, duplicate := seen[*parentID]; duplicate {
			return false
		}
		seen[*parentID] = struct{}{}
		ancestor, found := agentRegistry.Get(*parentID)
		if !found {
			return false
		}
		if agent.Same(ancestor, candidate) {
			return true
		}
		parentID = ancestor.SessionValue().Header().ParentSession
	}
	return false
}

var _ subagent.Starter = (*Service)(nil)
var _ subagent.ChildControl = (*Service)(nil)
