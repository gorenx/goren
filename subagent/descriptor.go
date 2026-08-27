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
}

// OneShotDescriptor identifies a session-backed terminal run.
type OneShotDescriptor struct {
	Version  int     `json:"version"`
	Mode     Mode    `json:"mode"`
	Provider string  `json:"provider"`
	Label    *string `json:"label,omitempty"`
}

func (OneShotDescriptor) descriptorVariant() {}

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

// BoundDescriptor identifies a child initialized from one durable parent
// binding. Mutable Bound configuration remains in its owning source.
type BoundDescriptor struct {
	Version  int    `json:"version"`
	Mode     Mode   `json:"mode"`
	Provider string `json:"provider"`
	Label    string `json:"label"`
}

func (BoundDescriptor) descriptorVariant() {}
