package systemprompt

import (
	"context"
	"sync/atomic"
)

const (
	promptHandleActive uint32 = iota
	promptHandleUnregistering
	promptHandleUnregistered
)

type promptHandleState struct {
	phase         atomic.Uint32
	unregisterErr error
}

func (state *promptHandleState) begin() (bool, error) {
	for {
		switch state.phase.Load() {
		case promptHandleUnregistering:
			return false, nil
		case promptHandleUnregistered:
			return false, state.unregisterErr
		default:
			if state.phase.CompareAndSwap(
				promptHandleActive,
				promptHandleUnregistering,
			) {
				return true, nil
			}
		}
	}
}

func (state *promptHandleState) finish(
	completed bool,
	unregisterErr error,
) {
	if !completed {
		state.phase.Store(promptHandleActive)
		return
	}
	state.unregisterErr = unregisterErr
	state.phase.Store(promptHandleUnregistered)
}

// PromptHandle owns one exact section, context provider, variable provider,
// Tool provider, or runtime-context suppressor in one Registry layer.
type PromptHandle struct {
	state promptHandleState
	owner *Registry
	kind  promptEntryKind
	name  string
	token *promptEntryToken
}

// Unregister removes only the prompt entry represented by this handle.
func (handle *PromptHandle) Unregister(requestContext context.Context) error {
	if handle == nil || handle.owner == nil || handle.token == nil {
		return nil
	}
	proceed, previousErr := handle.state.begin()
	if !proceed {
		return previousErr
	}
	completed, err := handle.owner.unregisterPrompt(
		requestContext,
		handle.kind,
		handle.name,
		handle.token,
	)
	handle.state.finish(completed, err)
	return err
}
