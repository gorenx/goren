package agent

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
)

// ModelSelection is one complete live provider/model choice.
type ModelSelection struct {
	Provider        string
	Model           string
	ReasoningEffort llm.ReasoningEffortID
}

// ModelSelectionSource resolves the live fallback used until this process
// explicitly selects a model for the Agent.
type ModelSelectionSource func() (ModelSelection, bool, error)

// ModelSelectionRef couples the requested next-step selection to the effective
// selection captured by the most recent prompt assembly.
type ModelSelectionRef struct {
	mu        sync.RWMutex
	requested *ModelSelection
	effective *ModelSelection
	source    ModelSelectionSource
}

// NewModelSelectionRef keeps an optional logged/default source live until an
// explicit SetCurrent selection takes precedence.
func NewModelSelectionRef(source ModelSelectionSource) *ModelSelectionRef {
	return &ModelSelectionRef{source: source}
}

// SetCurrent changes selection for the next step that enters prompt assembly.
func (selection *ModelSelectionRef) SetCurrent(selected *ModelSelection) {
	selection.mu.Lock()
	selection.requested = cloneSelection(selected)
	selection.mu.Unlock()
}

// Current returns the explicit next-step selection or resolves its live fallback.
func (selection *ModelSelectionRef) Current() (ModelSelection, bool, error) {
	selection.mu.RLock()
	selected := cloneSelection(selection.requested)
	source := selection.source
	selection.mu.RUnlock()
	if selected != nil {
		return *selected, true, nil
	}
	if source == nil {
		return ModelSelection{}, false, nil
	}
	return source()
}

// Assembled returns the selection captured for the most recent assembly.
func (selection *ModelSelectionRef) Assembled() (ModelSelection, bool) {
	selection.mu.RLock()
	defer selection.mu.RUnlock()
	if selection.effective == nil {
		return ModelSelection{}, false
	}
	return *selection.effective, true
}

// ModelSelectionPlugin keeps System Prompt variables and request routing on
// one step-consistent selection snapshot.
type ModelSelectionPlugin struct {
	plugin.Base
	selection *ModelSelectionRef
	prompt    promptSelectionMiddleware
	request   requestSelectionMiddleware
}

// NewModelSelectionPlugin constructs the two scoped Waterfall bindings owned
// by one model-selection lifecycle.
func NewModelSelectionPlugin(selection *ModelSelectionRef) (*ModelSelectionPlugin, error) {
	if selection == nil {
		return nil, errors.New("agent: model selection reference is required")
	}
	owner := &ModelSelectionPlugin{
		selection: selection,
	}
	owner.prompt.selection = selection
	owner.request.selection = selection
	return owner, nil
}

// Manifest declares prompt assembly and request routing Middleware.
func (owner *ModelSelectionPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "@deepseek-ai/dsh-agent/model-selection",
		Waterfalls: []plugin.WaterfallMiddlewareBinding{
			plugin.WaterfallOf(&owner.prompt),
			plugin.WaterfallOf(&owner.request),
		},
	}
}

// Apply validates startup cancellation before Middleware publication.
func (*ModelSelectionPlugin) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

// Dispose clears the last step snapshot without changing the live selection.
func (owner *ModelSelectionPlugin) Dispose(context.Context) error {
	owner.selection.mu.Lock()
	owner.selection.effective = nil
	owner.selection.mu.Unlock()
	return nil
}

type promptSelectionMiddleware struct {
	selection *ModelSelectionRef
}

func (middleware *promptSelectionMiddleware) Intercept(
	requestContext context.Context,
	input systemprompt.AssembleRequest,
	downstream plugin.WaterfallAction[
		systemprompt.AssembleRequest,
		systemprompt.PromptAssembly,
	],
) (systemprompt.PromptAssembly, error) {
	selected, found, selectionErr := middleware.selection.Current()
	if selectionErr != nil {
		return systemprompt.PromptAssembly{}, selectionErr
	}
	resolvedPrompt, resolveErr := downstream.Execute(requestContext, input)
	if resolveErr != nil {
		return systemprompt.PromptAssembly{}, resolveErr
	}
	middleware.selection.mu.Lock()
	if found {
		middleware.selection.effective = cloneSelection(&selected)
	} else {
		middleware.selection.effective = nil
	}
	middleware.selection.mu.Unlock()
	if !found {
		return resolvedPrompt, nil
	}
	if resolvedPrompt.Variables == nil {
		resolvedPrompt.Variables = make(map[string]systemprompt.VariableValue)
	}
	resolvedPrompt.Variables["provider"] = systemprompt.VariableValue{
		Value:   selected.Provider,
		Defined: true,
	}
	resolvedPrompt.Variables["model"] = systemprompt.VariableValue{
		Value:   selected.Model,
		Defined: true,
	}
	return resolvedPrompt, nil
}

type requestSelectionMiddleware struct {
	selection *ModelSelectionRef
}

func (middleware *requestSelectionMiddleware) Intercept(
	requestContext context.Context,
	input RequestNotice,
	downstream plugin.WaterfallAction[RequestNotice, RequestResolution],
) (RequestResolution, error) {
	resolved, resolveErr := downstream.Execute(requestContext, input)
	if resolveErr != nil {
		return RequestResolution{}, resolveErr
	}
	selected, found := middleware.selection.Assembled()
	if !found {
		return resolved, nil
	}
	resolved.Config.Provider = selected.Provider
	resolved.Config.Model = selected.Model
	resolved.Config.ReasoningEffort = selected.ReasoningEffort
	return resolved, nil
}

func cloneSelection(source *ModelSelection) *ModelSelection {
	if source == nil {
		return nil
	}
	copyValue := *source
	return &copyValue
}
