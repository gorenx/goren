package tools

import (
	"context"
	"errors"
	"fmt"
)

// ErrToolSetInactive reports that a Tool Set was closed with its owning layer.
var ErrToolSetInactive = errors.New("tools: Tool Set is inactive")

type toolSetState struct {
	name    string
	entries []*registeredTool
	active  bool
}

// ToolSetHandle owns one named, atomically replaceable group of Tool
// definitions in one ToolLayer.
type ToolSetHandle struct {
	owner *ToolLayer
	state *toolSetState
}

// OpenToolSet reserves one Tool Set identity without publishing an empty
// registry change.
func (owner *ToolLayer) OpenToolSet(
	requestContext context.Context,
	name string,
) (*ToolSetHandle, error) {
	if err := validateMutation(requestContext, "Tool Set", name); err != nil {
		return nil, err
	}
	_, unlock, err := owner.beginMutation(requestContext)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := owner.requireActive(); err != nil {
		return nil, err
	}
	owner.toolSetMutex.Lock()
	defer owner.toolSetMutex.Unlock()
	if owner.toolSets == nil {
		owner.toolSets = make(map[string]*toolSetState)
	}
	if _, present := owner.toolSets[name]; present {
		return nil, fmt.Errorf(
			"tools: Tool Set %q is already open in this layer",
			name,
		)
	}
	state := &toolSetState{
		name:   name,
		active: true,
	}
	owner.toolSets[name] = state
	return &ToolSetHandle{
		owner: owner,
		state: state,
	}, nil
}

// Replace compiles the complete candidate generation and publishes it in one
// registry mutation. A compilation failure leaves the current generation
// untouched.
func (handle *ToolSetHandle) Replace(
	requestContext context.Context,
	definitions []ToolDefinition,
) error {
	if handle == nil || handle.owner == nil || handle.state == nil {
		return ErrToolSetInactive
	}
	if err := validateMutationContext(requestContext); err != nil {
		return err
	}
	candidates := make([]*registeredTool, 0, len(definitions))
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		entry, err := handle.owner.registry.compileTool(definition)
		if err != nil {
			return err
		}
		if _, present := seen[entry.registrationName]; present {
			return fmt.Errorf(
				"tools: Tool Set %q contains duplicate tool %q",
				handle.state.name,
				entry.registrationName,
			)
		}
		seen[entry.registrationName] = struct{}{}
		candidates = append(candidates, entry)
	}

	eventContext, unlock, err := handle.owner.beginMutation(requestContext)
	if err != nil {
		return err
	}
	defer unlock()
	if err := handle.owner.requireActive(); err != nil {
		return err
	}
	handle.owner.toolSetMutex.Lock()
	current := handle.owner.toolSets[handle.state.name]
	if current != handle.state || !handle.state.active {
		handle.owner.toolSetMutex.Unlock()
		return ErrToolSetInactive
	}
	previous := append([]*registeredTool(nil), handle.state.entries...)
	mutation, replaceErr := handle.owner.registry.replaceToolSet(
		previous,
		candidates,
	)
	if replaceErr == nil {
		handle.state.entries = candidates
	} else if mutation.changed {
		handle.state.entries = nil
	}
	handle.owner.toolSetMutex.Unlock()
	if !mutation.changed {
		return replaceErr
	}
	if publishErr := handle.owner.publishChanged(eventContext); publishErr != nil {
		handle.owner.registry.rollbackToolSet(mutation)
		handle.owner.toolSetMutex.Lock()
		if handle.owner.toolSets[handle.state.name] == handle.state && handle.state.active {
			handle.state.entries = previous
		}
		handle.owner.toolSetMutex.Unlock()
		return errors.Join(replaceErr, publishErr)
	}
	return replaceErr
}

// Close removes the current Tool Set and releases its identity. It is safe to
// call repeatedly.
func (handle *ToolSetHandle) Close(requestContext context.Context) error {
	if handle == nil || handle.owner == nil || handle.state == nil {
		return nil
	}
	if err := validateMutationContext(requestContext); err != nil {
		return err
	}
	eventContext, unlock, err := handle.owner.beginMutation(requestContext)
	if err != nil {
		return err
	}
	defer unlock()
	handle.owner.toolSetMutex.Lock()
	current := handle.owner.toolSets[handle.state.name]
	if current != handle.state || !handle.state.active {
		handle.owner.toolSetMutex.Unlock()
		return nil
	}
	previous := append([]*registeredTool(nil), handle.state.entries...)
	handle.state.active = false
	handle.state.entries = nil
	delete(handle.owner.toolSets, handle.state.name)
	if len(handle.owner.toolSets) == 0 {
		handle.owner.toolSets = nil
	}
	handle.owner.toolSetMutex.Unlock()

	mutation, removeErr := handle.owner.registry.replaceToolSet(previous, nil)
	if removeErr != nil {
		return removeErr
	}
	if !mutation.changed {
		return nil
	}
	return handle.owner.publishChanged(eventContext)
}
