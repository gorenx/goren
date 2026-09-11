package systemprompt

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

var (
	// ErrAgent identifies an update addressed to another Agent.
	ErrAgent = errors.New("systemprompt: Agent identity mismatch")
	// ErrToolsNotReady means no tool definition baseline has been received.
	ErrToolsNotReady = errors.New("systemprompt: tool definitions are not ready")
)

// Runtime owns one Agent's prompt definitions and consumed tool definition view.
// Its Grain serializes access; it holds no framework or other owner references.
type Runtime struct {
	agentID     session.SessionID
	definitions Snapshot
	tools       *ToolDefinitions
}

// NewRuntime creates Agent-local state without activating resources or subscriptions.
func NewRuntime(identifier session.SessionID, definitions Snapshot) (*Runtime, error) {
	if identifier == "" {
		return nil, errors.New("systemprompt: Agent identity is required")
	}
	return &Runtime{
		agentID:     identifier,
		definitions: definitions,
	}, nil
}

// SetDefinitions applies an explicitly captured prompt definition baseline.
func (owner *Runtime) SetDefinitions(definitions Snapshot) {
	owner.definitions = definitions
}

// ApplyTools installs an authoritative complete baseline, preserving the previous
// view on error. The caller orders Registry reads and rejects stale deliveries.
func (owner *Runtime) ApplyTools(identifier session.SessionID, definitions ToolDefinitions) error {
	if identifier != owner.agentID {
		return ErrAgent
	}
	detached := ToolDefinitions{
		Schemas:    make([]llm.ToolSchema, len(definitions.Schemas)),
		KnownNames: slices.Clone(definitions.KnownNames),
	}
	seen := make(map[string]struct{}, len(definitions.Schemas))
	for index, schema := range definitions.Schemas {
		if schema.Name == "" || schema.Name == ToolOrderRest {
			return fmt.Errorf("systemprompt: invalid tool name %q", schema.Name)
		}
		if _, exists := seen[schema.Name]; exists {
			return fmt.Errorf("systemprompt: duplicate tool name %q", schema.Name)
		}
		seen[schema.Name] = struct{}{}
		copied, err := detachToolSchema(schema)
		if err != nil {
			return err
		}
		detached.Schemas[index] = copied
	}
	owner.tools = &detached
	return nil
}

// Assemble uses only this Runtime's consumed tools and registered prompt variables.
func (owner *Runtime) Assemble(ctx context.Context) (PromptAssembly, error) {
	err := validatePromptMutationContext(ctx)
	if err != nil {
		return PromptAssembly{}, err
	}
	if owner.tools == nil {
		return PromptAssembly{}, ErrToolsNotReady
	}
	requestTools := ToolDefinitions{
		Schemas:    make([]llm.ToolSchema, len(owner.tools.Schemas)),
		KnownNames: slices.Clone(owner.tools.KnownNames),
	}
	for index, schema := range owner.tools.Schemas {
		requestTools.Schemas[index] = llm.ToolSchema{
			Version:     schema.Version,
			Name:        schema.Name,
			Description: schema.Description,
			Parameters:  slices.Clone(schema.Parameters),
		}
	}
	return owner.definitions.Assemble(ctx, AssembleContext{
		Tools: requestTools,
	})
}
