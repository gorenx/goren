package continuation

import (
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
)

// MessageLeftInbox clears the waking admission guard for one message.
func (owner *Service) MessageLeftInbox(
	childAgent agent.Agent,
	messageID llm.MessageID,
) {
	owner.mutex.RLock()
	activeManager := owner.active
	owner.mutex.RUnlock()
	if activeManager != nil {
		activeManager.MessageLeftInbox(childAgent, messageID)
	}
}

// AgentDisposed removes state retained for an externally disposed exact Agent.
func (owner *Service) AgentDisposed(childAgent agent.Agent) {
	owner.mutex.RLock()
	activeManager := owner.active
	owner.mutex.RUnlock()
	if activeManager != nil {
		activeManager.AgentDisposed(childAgent)
	}
}
