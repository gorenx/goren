package execution

import (
	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/subagent"
)

// EventPublisher publishes Subagent lifecycle facts to the exact parent Agent.
// It does not persist Session events or own an execution state machine.
type EventPublisher interface {
	PublishStarted(agentregistry.Creation, subagent.Started)
	PublishEnded(agentregistry.Creation, subagent.Ended)
}
