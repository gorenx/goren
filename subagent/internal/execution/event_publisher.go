package execution

import (
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent"
)

// EventPublisher publishes Subagent lifecycle facts to the exact parent Agent.
// It does not persist Session events or own an execution state machine.
type EventPublisher interface {
	PublishStarted(agent.Agent, subagent.Started)
	PublishEnded(agent.Agent, subagent.Ended)
}
