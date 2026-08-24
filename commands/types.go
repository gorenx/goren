// Package commands owns the human slash-command registry, direct execution,
// and durable command lifecycle vocabulary.
package commands

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

const (
	// PluginName is the canonical Harness Commands Plugin name.
	PluginName = "@deepseek-ai/dsh-commands"
	// ServiceName preserves the canonical Cordis capability name.
	ServiceName = "commands"

	RunEventName  = "command/run"
	DoneEventName = "command/done"
)

// ID pairs one command/run event with its command/done event.
type ID string

// InputDescriptor advertises optional free-form input to command adapters.
type InputDescriptor struct {
	Hint   string `json:"hint"`
	Images bool   `json:"images,omitempty"`
}

// Descriptor is the handler-free command view returned to UI adapters.
type Descriptor struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Input       *InputDescriptor `json:"input,omitempty"`
}

// ResultKind identifies a direct command outcome.
type ResultKind string

const (
	ResultSuccess ResultKind = "success"
	ResultError   ResultKind = "error"
)

// Result is rendered directly by the dispatching UI. SourceEventSeq may only
// accompany a successful result.
type Result struct {
	Kind           ResultKind `json:"kind"`
	Text           *string    `json:"text,omitempty"`
	SourceEventSeq *int64     `json:"sourceEventSeq,omitempty"`
}

// Execution is one settled handler outcome and its durable pairing identity.
type Execution struct {
	CommandID ID     `json:"commandId"`
	Result    Result `json:"result"`
}

// Invocation is the exact command input after registry resolution and after
// command/run has committed.
type Invocation struct {
	CommandID ID
	Agent     agent.Agent
	RawInput  string
}

// Handler executes one resolved command without scheduling an Agent turn.
type Handler func(context.Context, Invocation) (Result, error)

// Definition is one owned command registration.
type Definition struct {
	Name        string
	Description string
	Input       *InputDescriptor
	RecordInput *bool
	Handler     Handler
}

// ExecuteOptions contains adapter admission facts used before a handler runs.
// The current Commands composition rejects image-capable registrations and
// rejects non-empty batches for every admitted command.
type ExecuteOptions struct {
	AttachmentCount int
}

// Registry is the Commands Service Definition consumed by command plugins and
// inbound adapters.
type Registry interface {
	plugin.Service
	Register(Definition) (*Registration, error)
	List(agent.Agent) []Descriptor
	Find(agent.Agent, string) (Definition, bool)
	Execute(context.Context, agent.Agent, string, ExecuteOptions) (*Execution, error)
}

// Source identifies who issued a durable command lifecycle.
type Source struct {
	Kind string `json:"kind"`
}

// Run records a resolved command before its handler starts.
type Run struct {
	CommandID ID      `json:"commandId"`
	Name      string  `json:"name"`
	Args      *string `json:"args,omitempty"`
	Source    Source  `json:"source"`
}

// Done records the paired normalized result or thrown/cancelled failure.
type Done struct {
	CommandID      ID         `json:"commandId"`
	Kind           ResultKind `json:"kind"`
	Text           *string    `json:"text,omitempty"`
	SourceEventSeq *int64     `json:"sourceEventSeq,omitempty"`
}

var (
	RunEvent  = session.DefineEvent[Run](RunEventName)
	DoneEvent = session.DefineEvent[Done](DoneEventName)
)
