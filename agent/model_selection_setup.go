package agent

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/systemprompt"
)

// ModelSelectionSetup binds one selection reference to prompt assembly and
// model-request resolution inside an Agent Scope.
type ModelSelectionSetup struct {
	selection *ModelSelectionRef
	prompt    promptSelectionMiddleware
	request   requestSelectionMiddleware
}

// NewModelSelectionSetup constructs one Agent-local selection Setup.
func NewModelSelectionSetup(
	selection *ModelSelectionRef,
) (*ModelSelectionSetup, error) {
	if selection == nil {
		return nil, errors.New("agent: model selection reference is required")
	}
	owner := &ModelSelectionSetup{
		selection: selection,
	}
	owner.prompt.selection = selection
	owner.request.selection = selection
	return owner, nil
}

// Apply registers both Middleware bindings and the selection cleanup resource.
func (owner *ModelSelectionSetup) Apply(
	_ context.Context,
	_ Agent,
	editor ScopeEditor,
) error {
	if err := editor.UsePromptAssembly(&owner.prompt); err != nil {
		return err
	}
	if err := editor.UseRequest(&owner.request); err != nil {
		return err
	}
	return editor.Own(&modelSelectionResource{
		selection: owner.selection,
	})
}

type modelSelectionResource struct {
	once      sync.Once
	selection *ModelSelectionRef
}

func (resource *modelSelectionResource) Close(context.Context) error {
	if resource == nil {
		return nil
	}
	resource.once.Do(func() {
		resource.selection.mu.Lock()
		resource.selection.effective = nil
		resource.selection.mu.Unlock()
	})
	return nil
}

type promptSelectionMiddleware struct {
	selection *ModelSelectionRef
}

func (middleware *promptSelectionMiddleware) InterceptAssembly(
	requestContext context.Context,
	input systemprompt.AssembleRequest,
	downstream systemprompt.AssemblyAction,
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

func (middleware *requestSelectionMiddleware) InterceptRequest(
	requestContext context.Context,
	input RequestNotice,
	downstream RequestAction,
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

var _ Setup = (*ModelSelectionSetup)(nil)
var _ systemprompt.AssemblyMiddleware = (*promptSelectionMiddleware)(nil)
var _ RequestMiddleware = (*requestSelectionMiddleware)(nil)
