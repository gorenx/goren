// Package subagents owns the offered Subagent application capabilities.
package subagents

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

// implementation is the common lifecycle contract for one Subagent mode.
// Mode-specific behavior stays behind this consumer-owned boundary.
type implementation interface {
	Mode() subagent.Mode
	Start(context.Context, subagent.StartCommand) (subagent.Execution, error)
	Interrupt(context.Context, session.SessionID) error
	Close(context.Context) error
}

// messenger owns delivery to a child that can accept later turns, including
// resolving a durable but currently inactive child.
type messenger interface {
	Send(
		context.Context,
		agent.Agent,
		session.SessionID,
		[]llm.ContentBlock,
		subagent.FollowupOptions,
	) (llm.MessageID, error)
}

// reporter owns child-to-parent message delivery.
type reporter interface {
	Report(
		context.Context,
		agent.Agent,
		[]llm.ContentBlock,
		subagent.ReportOptions,
	) (llm.MessageID, error)
}

type admissionState uint8

const (
	admissionInactive admissionState = iota
	admissionAccepting
	admissionClosing
	admissionClosed
)

// Service is the single Subagent application service published through the
// narrow Starter, ChildControl, and ParentReporter capability views.
type Service struct {
	mutex           sync.RWMutex
	state           admissionState
	activeCalls     sync.WaitGroup
	agents          agent.Registry
	executions      *sharedexecution.Registry
	implementations map[subagent.Mode]implementation
	closeOrder      []implementation
	messenger       messenger
	reporter        reporter
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
	messageService messenger,
	reportService reporter,
	implementations ...implementation,
) error {
	if agentRegistry == nil || executionRegistry == nil ||
		len(implementations) == 0 || messageService == nil ||
		reportService == nil {
		return errors.New(
			"subagent: Service requires Agent Registry, Execution Registry, " +
				"implementations, messenger, and reporter",
		)
	}
	implementationIndex := make(map[subagent.Mode]implementation, len(implementations))
	closeOrder := make([]implementation, 0, len(implementations))
	for _, candidate := range implementations {
		if candidate == nil || candidate.Mode() == "" {
			return errors.New("subagent: implementation is incomplete")
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
	owner.messenger = messageService
	owner.reporter = reportService
	owner.state = admissionAccepting
	return nil
}

// Start selects an implementation by the command's stable business mode and
// always returns the common Execution contract.
func (owner *Service) Start(
	requestContext context.Context,
	command subagent.StartCommand,
) (subagent.Execution, error) {
	candidate, beginErr := owner.beginImplementation(command.Mode())
	if beginErr != nil {
		return nil, beginErr
	}
	defer owner.activeCalls.Done()
	return candidate.Start(requestContext, command)
}

// Send delivers a later turn through the registered child messenger.
func (owner *Service) Send(
	requestContext context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	content []llm.ContentBlock,
	options subagent.FollowupOptions,
) (llm.MessageID, error) {
	messageService, beginErr := owner.beginMessenger()
	if beginErr != nil {
		return "", beginErr
	}
	defer owner.activeCalls.Done()
	return messageService.Send(
		requestContext,
		parentAgent,
		childID,
		content,
		options,
	)
}

// Interrupt performs common live-execution lookup and ancestor authorization,
// then delegates only the mode-specific interruption behavior.
func (owner *Service) Interrupt(
	requestContext context.Context,
	childID session.SessionID,
	authority subagent.InterruptAuthority,
) error {
	executionRegistry, implementationIndex, agentRegistry, beginErr :=
		owner.beginControl()
	if beginErr != nil {
		return beginErr
	}
	defer owner.activeCalls.Done()
	entry, found := executionRegistry.Find(childID)
	if !found {
		return nil
	}
	if authorizationErr := authorizeInterrupt(
		agentRegistry,
		entry,
		authority,
	); authorizationErr != nil {
		return authorizationErr
	}
	candidate, found := implementationIndex[entry.Mode]
	if !found {
		return fmt.Errorf(
			"subagent: live execution has no implementation for mode %q",
			entry.Mode,
		)
	}
	return candidate.Interrupt(requestContext, childID)
}

// Report delivers child content to its direct parent.
func (owner *Service) Report(
	requestContext context.Context,
	childAgent agent.Agent,
	content []llm.ContentBlock,
	options subagent.ReportOptions,
) (llm.MessageID, error) {
	reportService, beginErr := owner.beginReporter()
	if beginErr != nil {
		return "", beginErr
	}
	defer owner.activeCalls.Done()
	return reportService.Report(
		requestContext,
		childAgent,
		content,
		options,
	)
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
	owner.messenger = nil
	owner.reporter = nil
	owner.closeErr = closeErr
	owner.state = admissionClosed
	close(owner.closed)
	owner.mutex.Unlock()
	return closeErr
}

func (owner *Service) beginImplementation(
	mode subagent.Mode,
) (implementation, error) {
	owner.mutex.RLock()
	defer owner.mutex.RUnlock()
	if owner.state != admissionAccepting {
		return nil, unavailable()
	}
	candidate, found := owner.implementations[mode]
	if !found {
		return nil, fmt.Errorf(
			"subagent: no implementation registered for mode %q",
			mode,
		)
	}
	owner.activeCalls.Add(1)
	return candidate, nil
}

func (owner *Service) beginMessenger() (messenger, error) {
	owner.mutex.RLock()
	defer owner.mutex.RUnlock()
	if owner.state != admissionAccepting {
		return nil, unavailable()
	}
	owner.activeCalls.Add(1)
	return owner.messenger, nil
}

func (owner *Service) beginReporter() (reporter, error) {
	owner.mutex.RLock()
	defer owner.mutex.RUnlock()
	if owner.state != admissionAccepting {
		return nil, unavailable()
	}
	owner.activeCalls.Add(1)
	return owner.reporter, nil
}

func (owner *Service) beginControl() (
	*sharedexecution.Registry,
	map[subagent.Mode]implementation,
	agent.Registry,
	error,
) {
	owner.mutex.RLock()
	defer owner.mutex.RUnlock()
	if owner.state != admissionAccepting {
		return nil, nil, nil, unavailable()
	}
	owner.activeCalls.Add(1)
	return owner.executions, owner.implementations, owner.agents, nil
}

func unavailable() error {
	return &subagent.Error{
		Code:    subagent.ErrorDraining,
		Message: "Subagents are closing",
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
var _ subagent.ParentReporter = (*Service)(nil)
