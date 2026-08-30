package extension

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent"
)

// Setup applies the Registry's current Extension view to one child Agent.
type Setup struct {
	registry *Registry
	selected []string
}

// NewSetup constructs the common Extension Setup used by every child.
func NewSetup(owner *Registry) *Setup {
	return &Setup{
		registry: owner,
	}
}

// NewSelectedSetup validates and snapshots named Extension selection.
func NewSelectedSetup(owner *Registry, names []string) (*Setup, error) {
	if owner == nil {
		return nil, errors.New("subagent: Extension Registry is unavailable")
	}
	if _, err := owner.selected(names); err != nil {
		return nil, err
	}
	return &Setup{
		registry: owner,
		selected: append([]string{}, names...),
	}, nil
}

// Apply registers each Extension as a separately owned nested Setup. A
// registration removed before outer Scope commit invalidates the draft.
func (owner *Setup) Apply(
	requestContext context.Context,
	_ agent.Agent,
	editor agent.ScopeEditor,
) error {
	if owner == nil || owner.registry == nil || editor == nil {
		return errors.New("subagent: Extension Setup is unavailable")
	}
	var registrations []*registration
	if owner.selected == nil {
		registrations = owner.registry.common()
	} else {
		var err error
		registrations, err = owner.registry.selected(owner.selected)
		if err != nil {
			return err
		}
	}
	validity := &setupValidity{}
	if err := editor.Check(validity); err != nil {
		return err
	}
	for _, record := range registrations {
		record.mutex.Lock()
		removed := record.state == registrationRemoved
		record.mutex.Unlock()
		if removed {
			if owner.selected != nil {
				return &subagent.Error{
					Code:    subagent.ErrorUnknownExtension,
					Message: "a selected child Extension is no longer registered",
				}
			}
			continue
		}
		resources, err := editor.ApplyNestedSetup(requestContext, record.extension)
		if err != nil {
			return err
		}
		binding := &extensionBinding{
			resources: resources,
			validity:  validity,
		}
		record.mutex.Lock()
		record.installations = append(record.installations, binding)
		removed = record.state == registrationRemoved
		record.mutex.Unlock()
		if err = editor.Own(binding); err != nil {
			return err
		}
		if removed {
			validity.invalidate()
			if err = binding.Close(context.WithoutCancel(requestContext)); err != nil {
				return err
			}
		}
	}
	return nil
}

var _ agent.Setup = (*Setup)(nil)
