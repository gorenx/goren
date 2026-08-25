// Package inprocess owns the shared one-shot child driver used by in-process
// Subagent Providers. Continuable child lifecycle remains owned by continuation.
package inprocess

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/childscope"
	"github.com/gorenx/goren/subagent/internal/lineage"
)

// Options contains Provider-owned creation data for one in-process Run.
type Options struct {
	Seed []session.Event
}

// Driver creates and drives one published local child for exactly one turn.
type Driver struct {
	agents       agent.Registry
	constructor  agent.Constructor
	scopeBuilder *childscope.OneShotBuilder
}

// New constructs an in-process one-shot Driver.
func New(
	agentRegistry agent.Registry,
	agentConstructor agent.Constructor,
	childScopeBuilder *childscope.OneShotBuilder,
) (*Driver, error) {
	if agentRegistry == nil {
		return nil, errors.New("subagent: in-process Driver requires Agent Registry")
	}
	if agentConstructor == nil {
		return nil, errors.New("subagent: in-process Driver requires Agent Constructor")
	}
	if childScopeBuilder == nil {
		return nil, errors.New("subagent: in-process Driver requires child Scope builder")
	}
	return &Driver{
		agents:       agentRegistry,
		constructor:  agentConstructor,
		scopeBuilder: childScopeBuilder,
	}, nil
}

// Start creates a published child and transfers its exact lifecycle through Run.
func (owner *Driver) Start(
	requestContext context.Context,
	request subagent.ResolvedStartRequest,
	settings Options,
) (subagent.Run, error) {
	if requestContext == nil {
		return nil, errors.New("subagent: in-process Start context is nil")
	}
	if requestErr := requestContext.Err(); requestErr != nil {
		return nil, requestErr
	}
	if request.Parent == nil || !owner.agents.Contains(request.Parent) {
		return nil, &subagent.Error{
			Code:    subagent.ErrorUnauthorized,
			Message: "in-process Start requires the exact live parent Agent",
		}
	}
	childLineage, lineageErr := lineage.From(request.Parent, request.MaxDepth)
	if lineageErr != nil {
		return nil, lineageErr
	}
	childID, identityErr := newSessionID()
	if identityErr != nil {
		return nil, identityErr
	}
	prompt, messageErr := llm.NewUserMessage(llm.UserMessageInput{
		Content: request.Prompt,
		Source: llm.UserMessageSource{
			Kind: "user",
		},
	})
	if messageErr != nil {
		return nil, fmt.Errorf("subagent: create one-shot prompt: %w", messageErr)
	}
	seed := cloneEvents(settings.Seed)
	boundary := int64(len(seed))
	descriptor := newDescriptorAppender(request.Descriptor)
	runPlugins := []plugin.Plugin{
		descriptor,
	}
	var structured *structuredCapture
	if len(request.OutputSchema) != 0 {
		structured = newStructuredCapture(request.OutputSchema)
		runPlugins = append(runPlugins, structured)
	}
	initiatedContext, contextErr := agent.WithInitiator(
		requestContext,
		request.Parent,
	)
	if contextErr != nil {
		return nil, contextErr
	}
	handle, createErr := owner.constructor.Create(
		initiatedContext,
		agent.CreateOptions{
			SessionID:    childID,
			Metadata:     childLineage.Metadata(boundary),
			Seed:         seed,
			AgentOptions: childLineage.AgentOptions(request.AgentOptions),
			Provisioner: owner.scopeBuilder.Provisioner(
				childscope.OneShotInput{
					Persona:    request.Persona,
					ToolFilter: request.ToolFilter,
					Plugins:    runPlugins,
				},
			),
			RuntimeParent: request.Parent,
		},
	)
	if createErr != nil {
		return nil, createErr
	}
	return newRun(
		requestContext,
		handle,
		prompt,
		boundary,
		structured,
	), nil
}

func cloneEvents(source []session.Event) []session.Event {
	if source == nil {
		return nil
	}
	detached := make([]session.Event, len(source))
	for index, eventValue := range source {
		detached[index] = eventValue
		detached[index].Data = append([]byte(nil), eventValue.Data...)
		if eventValue.SourceEventSeqs != nil {
			sequences := append([]int64(nil), (*eventValue.SourceEventSeqs)...)
			detached[index].SourceEventSeqs = &sequences
		}
		if eventValue.SurfaceOp != nil {
			operation := *eventValue.SurfaceOp
			detached[index].SurfaceOp = &operation
		}
	}
	return detached
}
