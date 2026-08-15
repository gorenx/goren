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

// ModelSelectionRef couples mutable next-step selection to the value captured
// for the current prompt assembly.
type ModelSelectionRef struct {
	mu        sync.RWMutex
	current   *ModelSelection
	assembled *ModelSelection
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
	selection.current = cloneSelection(selected)
	selection.mu.Unlock()
}

// Current returns the explicit next-step selection or resolves its live fallback.
func (selection *ModelSelectionRef) Current() (ModelSelection, bool, error) {
	selection.mu.RLock()
	selected := cloneSelection(selection.current)
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
	if selection.assembled == nil {
		return ModelSelection{}, false
	}
	return *selection.assembled, true
}

// InstallModelSelection keeps System Prompt variables and request routing on
// one step-consistent selection snapshot.
func InstallModelSelection(agentScope *plugin.Scope, selection *ModelSelectionRef) (plugin.Disposer, error) {
	if agentScope == nil || selection == nil {
		return nil, errors.New("agent: model selection Scope and reference are required")
	}
	releaseAssembly, err := systemprompt.OnAssemble(agentScope,
		func(requestContext context.Context, _ *systemprompt.PromptAssembly, _ systemprompt.AssembleContext, downstream systemprompt.AssembleNext) (systemprompt.PromptAssembly, error) {
			selected, found, selectionErr := selection.Current()
			if selectionErr != nil {
				return systemprompt.PromptAssembly{}, selectionErr
			}
			resolvedPrompt, resolveErr := downstream(requestContext)
			if resolveErr != nil {
				return systemprompt.PromptAssembly{}, resolveErr
			}
			selection.mu.Lock()
			if found {
				selection.assembled = cloneSelection(&selected)
			} else {
				selection.assembled = nil
			}
			selection.mu.Unlock()
			if !found {
				return resolvedPrompt, nil
			}
			if resolvedPrompt.Variables == nil {
				resolvedPrompt.Variables = make(map[string]systemprompt.VariableValue)
			}
			resolvedPrompt.Variables["provider"] = systemprompt.VariableValue{Value: selected.Provider, Defined: true}
			resolvedPrompt.Variables["model"] = systemprompt.VariableValue{Value: selected.Model, Defined: true}
			return resolvedPrompt, nil
		})
	if err != nil {
		return nil, err
	}
	releaseRequest, err := OnRequest(agentScope,
		func(requestContext context.Context, _ RequestNotice, downstream RequestNext) (llm.CallConfig, error) {
			resolved, resolveErr := downstream(requestContext)
			if resolveErr != nil {
				return llm.CallConfig{}, resolveErr
			}
			selected, found := selection.Assembled()
			if !found {
				return resolved, nil
			}
			resolved.Provider = selected.Provider
			resolved.Model = selected.Model
			resolved.ReasoningEffort = selected.ReasoningEffort
			return resolved, nil
		})
	if err != nil {
		return nil, errors.Join(err, releaseAssembly(context.Background()))
	}
	return func(closeContext context.Context) error {
		return errors.Join(releaseRequest(closeContext), releaseAssembly(closeContext))
	}, nil
}

func cloneSelection(source *ModelSelection) *ModelSelection {
	if source == nil {
		return nil
	}
	copyValue := *source
	return &copyValue
}
