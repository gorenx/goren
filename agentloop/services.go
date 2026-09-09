package agentloop

import (
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop/internal/modelrequest"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

// Services supplies capabilities for one serialized business call. A receiver
// obtains fresh Services with its result; business objects never retain them.
type Services struct {
	Conversation Conversation
	Prompts      Prompts
	Middleware   Middleware
	Models       Models
	Tools        ToolCalls
	Observer     Observer
}

// Conversation submits Session operations without blocking the business caller.
// A nil submission error promises exactly one later receiver invocation. A
// submission error means no receiver invocation will follow. Committed data may
// still exist after a delivery error; the business owner decides recovery.
type Conversation interface {
	Commit(session.WritePlan, CommitReceiver) error
	Read(SnapshotReceiver) error
	Flush(session.WriteBarrier, FlushReceiver) error
}

// CommitReceiver accepts the complete result of its own Session write.
type CommitReceiver interface {
	SessionCommitted(Services, session.WriteResult, error)
}

// SnapshotReceiver accepts the Session snapshot requested by the business flow.
type SnapshotReceiver interface {
	SessionRead(Services, session.Snapshot, error)
}

// FlushReceiver accepts the actual durable Session prefix.
type FlushReceiver interface {
	SessionFlushed(Services, session.WriteBarrier, error)
}

// Prompts assembles prompt data; it does not assemble conversation history.
type Prompts interface {
	Assemble(PromptReceiver) error
}

// PromptReceiver receives the result of one prompt assembly.
type PromptReceiver interface {
	PromptAssembled(Services, systemprompt.PromptAssembly, error)
}

// Middleware invokes only the hook requested by the business flow.
type Middleware interface {
	PreStep(PreStep, PreStepReceiver) error
	TurnStopping(agent.TurnStoppingInput, StoppingReceiver) error
	RequestError(agent.RequestFailure, RecoveryReceiver) error
}

// PreStepReceiver consumes admission before preparing Step context.
type PreStepReceiver interface {
	PreStepResolved(Services, agent.PreStepResolved)
}

// StoppingReceiver rechecks business input after the stopping hook finishes.
type StoppingReceiver interface {
	TurnStoppingCompleted(Services, agent.TurnStoppingCompleted)
}

// RecoveryReceiver consumes the recovery middleware's actual decision.
type RecoveryReceiver interface {
	RequestErrorResolved(Services, agent.RequestErrorResolved)
}

// Models transports one Agent's active generation, without owning request
// reconstruction, recovery decisions, Session facts or Step progression.
type Models interface {
	Generate(llm.Input, GenerationReceiver) error
	Read(ChunkReceiver) error
	Close(StreamReceiver) error
}

// GenerationReceiver receives the actual configuration used to open a stream.
type GenerationReceiver interface {
	GenerationStarted(Services, modelrequest.GenerationStarted)
}

// ChunkReceiver receives one requested stream item or a read failure.
type ChunkReceiver interface {
	ChunkRead(Services, modelrequest.ChunkRead, error)
}

// StreamReceiver receives resource closure, independently of provider finish.
type StreamReceiver interface {
	StreamClosed(Services, error)
}

// Observer exposes execution outcomes without taking ownership of the workflow.
type Observer interface {
	ExecutionFailed(error)
	ExecutionFinished()
}

// ToolCalls transports individual tool operations. Batch ordering and result
// acceptance belong to the calling business flow, never the implementation.
type ToolCalls interface {
	// Configure applies pending tools and Prompt definitions before this Turn prepares input.
	Configure(ConfigurationReceiver) error
	Prepare(tools.ToolExecutionInput, ToolReceiver) error
	Dispatch(agentmessage.CallID, ToolReceiver) error
	Finalize(agentmessage.CallID, ToolReceiver) error
	Release(agentmessage.CallID, ToolReceiver) error
}

// ToolReceiver consumes the actual result of the requested tool operation.
type ToolReceiver interface {
	ToolPrepared(Services, tools.ScheduledToolStage, error)
	ToolDispatched(Services, error)
	ToolFinalized(Services, tools.ToolExecutionResult, error)
	ToolReleased(Services, error)
}

// ConfigurationReceiver confirms that tools and their Prompt definitions agree.
type ConfigurationReceiver interface {
	ToolsConfigured(Services, error)
}
