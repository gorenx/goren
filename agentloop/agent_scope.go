package agentloop

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

type scopePhase uint8

const (
	// scopeOpen admits Setup, event, and Waterfall calls.
	scopeOpen scopePhase = iota
	// scopeClosing rejects new calls while admitted calls drain.
	scopeClosing
	// scopeClosed has released every Scope-owned resource.
	scopeClosed
)

// agentScope owns all Agent-local capability layers and committed resources
// for one exact Agent. It has no Plugin or Registry back-reference.
type agentScope struct {
	events     agentEvents
	waterfalls agentWaterfalls
	prompts    *systemprompt.PromptLayer
	tools      *tools.ToolLayer

	setupMutex   sync.Mutex
	mutex        sync.RWMutex
	phase        scopePhase
	calls        sync.WaitGroup
	resources    []*scopeResources
	observers    []*scopeRegistration[agent.AgentEventObserver]
	preStep      []*scopeRegistration[agent.PreStepMiddleware]
	requests     []*scopeRegistration[agent.RequestMiddleware]
	requestError []*scopeRegistration[agent.RequestErrorMiddleware]
	closeOnce    sync.Once
	closeDone    chan struct{}
	closeErr     error
}

func newAgentScope(
	events agentEvents,
	waterfalls agentWaterfalls,
	prompts *systemprompt.PromptLayer,
	toolLayer *tools.ToolLayer,
) (*agentScope, error) {
	if events == nil || waterfalls == nil || prompts == nil || toolLayer == nil {
		return nil, errors.New("agentloop: Agent Scope dependencies are incomplete")
	}
	return &agentScope{
		events:     events,
		waterfalls: waterfalls,
		prompts:    prompts,
		tools:      toolLayer,
		closeDone:  make(chan struct{}),
	}, nil
}

// Close releases committed Setup resources and capability layers in reverse
// ownership order. Concurrent callers receive the same completed result.
func (owner *agentScope) Close(closeContext context.Context) error {
	if owner == nil {
		return nil
	}
	if closeContext == nil {
		closeContext = context.Background()
	}
	owner.closeOnce.Do(func() {
		go owner.finishClose(context.WithoutCancel(closeContext))
	})
	select {
	case <-owner.closeDone:
		return owner.closeErr
	case <-closeContext.Done():
		return context.Cause(closeContext)
	}
}

func (owner *agentScope) finishClose(closeContext context.Context) {
	owner.mutex.Lock()
	owner.phase = scopeClosing
	owner.mutex.Unlock()
	owner.calls.Wait()
	owner.setupMutex.Lock()
	owner.mutex.Lock()
	resources := append([]*scopeResources(nil), owner.resources...)
	owner.resources = nil
	owner.mutex.Unlock()
	owner.setupMutex.Unlock()
	var closeErr error
	for index := len(resources) - 1; index >= 0; index-- {
		closeErr = errors.Join(closeErr, resources[index].Close(closeContext))
	}
	closeErr = errors.Join(closeErr, owner.tools.Close(closeContext))
	closeErr = errors.Join(closeErr, owner.prompts.Close(closeContext))
	owner.mutex.Lock()
	owner.closeErr = closeErr
	owner.phase = scopeClosed
	owner.observers = nil
	owner.preStep = nil
	owner.requests = nil
	owner.requestError = nil
	owner.mutex.Unlock()
	close(owner.closeDone)
}

func (owner *agentScope) beginCall() (func(), error) {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.phase != scopeOpen {
		if owner.phase == scopeClosed {
			return nil, errors.New("agentloop: Agent Scope is closed")
		}
		return nil, errors.New("agentloop: Agent Scope is closing")
	}
	owner.calls.Add(1)
	return owner.calls.Done, nil
}

func (owner *agentScope) toolRuntime() tools.ToolRuntime {
	return owner.tools
}

func (owner *agentScope) promptAssembler() systemprompt.Assembler {
	return owner.prompts
}

var _ agent.Scope = (*agentScope)(nil)
