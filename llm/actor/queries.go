package llmactor

import (
	"time"

	"github.com/gorenx/goren/llm"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Query identifies a bounded metadata operation and its result destination.
// It carries no generation identity because metadata queries do not own streams.
type Query struct {
	RequestID RequestID
	ReplyTo   *goakt.GrainIdentity
	Deadline  time.Time
}

// ListModels requests the selected provider's current model catalog.
type ListModels struct {
	Query
	Provider string
}

// ModelsListed returns detached model metadata.
type ModelsListed struct {
	RequestID RequestID
	Models    []llm.ModelInfo
	Err       error
}

// ResolveModel requests exact capabilities without opening a model stream.
type ResolveModel struct {
	Query
	Provider string
	Model    string
}

// ModelResolved returns exact model capabilities.
type ModelResolved struct {
	RequestID RequestID
	Info      llm.ResolvedModelInfo
	Err       error
}

// ResolveConfig validates a proposed configuration without updating Agent settings.
type ResolveConfig struct {
	Query
	Config llm.CallConfig
}

// ConfigResolved returns the effective proposal after exact-model validation.
type ConfigResolved struct {
	RequestID RequestID
	Config    llm.CallConfig
	Err       error
}

// CancelQuery interrupts the metadata operation identified by RequestID.
// The original query reports its own completion; this command is not a close acknowledgement.
type CancelQuery struct {
	RequestID RequestID
	Cause     error
}
