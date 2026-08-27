package continuable

import (
	"errors"

	"github.com/gorenx/goren/subagent"
)

func continuableDescriptor(
	seedBuilder string,
	label string,
	request subagent.ContinuableOptions,
) (subagent.ContinuableDescriptor, error) {
	if request.AgentOptions == nil {
		return subagent.ContinuableDescriptor{}, errors.New(
			"subagent: Continuable descriptor requires Agent options",
		)
	}
	descriptorData, snapshotErr := subagent.SnapshotDescriptor(
		subagent.ContinuableDescriptor{
			Provider:      seedBuilder,
			Label:         label,
			AgentProvider: stringPointer(request.AgentOptions.Provider),
			AgentModel:    stringPointer(request.AgentOptions.Model),
			Persona:       request.Persona,
			ToolFilter:    request.ToolFilter,
		},
	)
	if snapshotErr != nil {
		return subagent.ContinuableDescriptor{}, snapshotErr
	}
	descriptor, matches := descriptorData.DescriptorValue().(subagent.ContinuableDescriptor)
	if !matches {
		return subagent.ContinuableDescriptor{}, errors.New(
			"subagent: Continuable descriptor snapshot changed variant",
		)
	}
	return descriptor, nil
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
