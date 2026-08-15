//go:build contract

// Package fixture supplies contract-test doubles shared across fixed-source checks.
package fixture

import (
	"context"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

// Agent is the smallest live Agent stand-in needed by interaction contracts.
type Agent struct {
	Identifier   session.SessionID
	Conversation *session.Session
	AgentScope   *plugin.Scope

	mu       sync.Mutex
	injected []llm.UserMessage
}

func (subject *Agent) ID() session.SessionID                       { return subject.Identifier }
func (*Agent) OptionsValue() agent.Options                         { return agent.Options{} }
func (subject *Agent) SessionValue() *session.Session              { return subject.Conversation }
func (*Agent) InboxValue() *agent.Inbox                            { return nil }
func (*Agent) StatusValue() agent.Status                           { return agent.StatusIdle }
func (subject *Agent) ScopeValue() *plugin.Scope                   { return subject.AgentScope }
func (*Agent) Cancel(agent.CancelCause, agent.CancelOptions)       {}
func (*Agent) WhenIdle(context.Context) error                      { return nil }
func (*Agent) Followup(llm.UserMessage) error                      { return nil }
func (*Agent) Steer(llm.UserMessage) error                         { return nil }
func (*Agent) Send(llm.UserMessage, agent.InboxTarget, bool) error { return nil }
func (subject *Agent) Inject(messageValue llm.UserMessage) error {
	subject.mu.Lock()
	subject.injected = append(subject.injected, messageValue)
	subject.mu.Unlock()
	return nil
}
func (*Agent) RunMaintenance(requestContext context.Context, task agent.MaintenanceTask) error {
	return task.Run(requestContext)
}

// Injected returns a detached snapshot of policy notices.
func (subject *Agent) Injected() []llm.UserMessage {
	subject.mu.Lock()
	defer subject.mu.Unlock()
	detached := make([]llm.UserMessage, len(subject.injected))
	for index, messageValue := range subject.injected {
		copyValue, err := llm.CloneUserMessage(messageValue)
		if err == nil {
			detached[index] = copyValue
		}
	}
	return detached
}
