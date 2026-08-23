package continuation

import (
	"errors"
	"fmt"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func continuableDescriptor(
	providerName string,
	label string,
	request subagent.ContinuableRequest,
) (subagent.ContinuableDescriptor, error) {
	if request.AgentOptions == nil {
		return subagent.ContinuableDescriptor{}, errors.New(
			"subagent: continuable descriptor requires resolved Agent options",
		)
	}
	descriptorData, snapshotErr := subagent.SnapshotDescriptor(
		subagent.ContinuableDescriptor{
			Provider:      providerName,
			Label:         label,
			AgentProvider: stringPointer(request.AgentOptions.Provider),
			AgentModel:    stringPointer(request.AgentOptions.Model),
			Persona:       cloneString(request.Persona),
			ToolFilter:    request.ToolFilter,
		},
	)
	if snapshotErr != nil {
		return subagent.ContinuableDescriptor{}, snapshotErr
	}
	descriptor, matches := descriptorData.DescriptorValue().(subagent.ContinuableDescriptor)
	if !matches {
		return subagent.ContinuableDescriptor{}, errors.New(
			"subagent: continuable descriptor snapshot changed variant",
		)
	}
	return descriptor, nil
}

func descriptorSeed(
	childID session.SessionID,
	providerSeed []session.Event,
	descriptor subagent.ContinuableDescriptor,
) ([]session.Event, error) {
	staged, stageErr := session.New(
		childID,
		session.CreateOptions{
			Seed: providerSeed,
		},
	)
	if stageErr != nil {
		return nil, stageErr
	}
	descriptorData, snapshotErr := subagent.SnapshotDescriptor(descriptor)
	if snapshotErr != nil {
		return nil, snapshotErr
	}
	if _, appendErr := session.AppendSerialized(
		staged,
		subagent.DescriptorEvent,
		descriptorData,
	); appendErr != nil {
		return nil, appendErr
	}
	return staged.Events(), nil
}

func (owner *Manager) prepareProvider(
	providerName string,
) (subagent.ContinuableProvider, error) {
	candidate, found := owner.dependencies.Providers.GetProvider(providerName)
	if !found {
		return nil, &subagent.Error{
			Code:    subagent.ErrorNoProvider,
			Message: fmt.Sprintf("no subagent Provider registered for %q", providerName),
		}
	}
	continuationProvider, supported := candidate.(subagent.ContinuableProvider)
	if !supported {
		return nil, &subagent.Error{
			Code: subagent.ErrorUnsupportedCapability,
			Message: fmt.Sprintf(
				"subagent Provider %q does not support continuable children",
				providerName,
			),
		}
	}
	return continuationProvider, nil
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
