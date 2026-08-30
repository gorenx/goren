package agent

import (
	"sync"

	"github.com/gorenx/goren/llm"
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
	return &ModelSelectionRef{
		source: source,
	}
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

func cloneSelection(source *ModelSelection) *ModelSelection {
	if source == nil {
		return nil
	}
	copyValue := *source
	return &copyValue
}
