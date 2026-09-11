package llmactor

import (
	"github.com/gorenx/goren/llm"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// CallID identifies one generation for consistent routing; it is not persisted.
type CallID string

// RequestID correlates one operation with its explicit result message.
type RequestID string

// Delivery identifies the operation and its receiving Grain. Router forwarding
// does not retain a request response channel, so results use ReplyTo explicitly.
type Delivery struct {
	CallID    CallID
	RequestID RequestID
	ReplyTo   *goakt.GrainIdentity
}

// Generation is one model generation with an explicit configuration choice.
type Generation struct {
	Delivery
	Settings Settings
	Input    llm.Input
}

// Settings selects the parameters for one generation. Only the two value forms
// below are accepted; selecting an Agent never mutates its registered settings.
type Settings interface {
	generationSettings()
}

// AgentConfig selects the latest registered configuration of one Agent.
type AgentConfig struct {
	AgentID string
}

// Parameters supplies complete parameters for this call without changing Agent settings.
type Parameters llm.CallConfig

func (AgentConfig) generationSettings() {}
func (Parameters) generationSettings()  {}

// GenerationStarted reports resolved metadata and the actual opening result.
// Resources must still be closed even when opening returned an error.
type GenerationStarted struct {
	CallID      CallID
	RequestID   RequestID
	Config      llm.CallConfig
	Defaults    llm.CallConfigAdapterDefaults
	Context     *llm.ModelContext
	RetryPolicy llm.RetryPolicy
	Err         error
}

// ReadChunk requests one item after the previous item has been processed.
type ReadChunk struct{ Delivery }

// ChunkRead reports one read without claiming the stream has closed.
type ChunkRead struct {
	CallID    CallID
	RequestID RequestID
	Chunk     llm.StreamChunk
	Found     bool
	Err       error
}

// CancelCall interrupts I/O without claiming resource release.
type CancelCall struct {
	Delivery
	Cause error
}

// CancellationAccepted confirms that cancellation was applied to the call.
type CancellationAccepted struct {
	CallID    CallID
	RequestID RequestID
	Err       error
}

// CloseCall releases the call after any in-flight I/O has returned.
type CloseCall struct{ Delivery }

// CallClosed confirms actual resource release when Err is nil.
type CallClosed struct {
	CallID    CallID
	RequestID RequestID
	Err       error
}

// RoutingKey keeps generation operations together and distributes one-shot queries.
func RoutingKey(message any) string {
	switch input := message.(type) {
	case CancelQuery:
		return string(input.RequestID)
	case ListModels:
		return string(input.RequestID)
	case ResolveModel:
		return string(input.RequestID)
	case ResolveConfig:
		return string(input.RequestID)
	case Generation:
		return string(input.CallID)
	case ReadChunk:
		return string(input.CallID)
	case CancelCall:
		return string(input.CallID)
	case CloseCall:
		return string(input.CallID)
	default:
		return ""
	}
}
