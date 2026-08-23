package subagent

import (
	"github.com/gorenx/goren/tools"
)

const (
	// DescriptorEventName is the durable model-hidden child identity event.
	DescriptorEventName = "subagent/descriptor"
	// DescriptorVersion is the current complete descriptor schema version.
	DescriptorVersion = 2
)

// Descriptor is the supported durable identity union.
type Descriptor interface {
	descriptorVariant()
	DescriptorVersion() int
	DescriptorMode() Mode
	ProviderName() string
}

// OneShotDescriptor identifies a session-backed terminal run.
type OneShotDescriptor struct {
	Version  int     `json:"version"`
	Mode     Mode    `json:"mode"`
	Provider string  `json:"provider"`
	Label    *string `json:"label,omitempty"`
}

func (OneShotDescriptor) descriptorVariant() {}

// DescriptorVersion returns the persisted schema version.
func (value OneShotDescriptor) DescriptorVersion() int {
	return value.Version
}

// DescriptorMode returns ModeOneShot.
func (OneShotDescriptor) DescriptorMode() Mode {
	return ModeOneShot
}

// ProviderName returns the establishing Provider name.
func (value OneShotDescriptor) ProviderName() string {
	return value.Provider
}

// ContinuableDescriptor identifies a resumable child and its cold-resume
// composition inputs.
type ContinuableDescriptor struct {
	Version       int                    `json:"version"`
	Mode          Mode                   `json:"mode"`
	Provider      string                 `json:"provider"`
	Label         string                 `json:"label"`
	AgentProvider *string                `json:"agentProvider,omitempty"`
	AgentModel    *string                `json:"agentModel,omitempty"`
	Persona       *string                `json:"persona,omitempty"`
	ToolFilter    *tools.ToolRestriction `json:"toolFilter,omitempty"`
}

func (ContinuableDescriptor) descriptorVariant() {}

// DescriptorVersion returns the persisted schema version.
func (value ContinuableDescriptor) DescriptorVersion() int {
	return value.Version
}

// DescriptorMode returns ModeContinuable.
func (ContinuableDescriptor) DescriptorMode() Mode {
	return ModeContinuable
}

// ProviderName returns the establishing Provider name.
func (value ContinuableDescriptor) ProviderName() string {
	return value.Provider
}
