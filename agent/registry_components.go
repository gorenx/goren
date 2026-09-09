package agent

import (
	"errors"

	"github.com/gorenx/goren/session"
)

// ComponentKind identifies a required Agent capability, not a startup phase.
type ComponentKind uint8

const (
	// SessionComponent supplies the opened or restored conversation.
	SessionComponent ComponentKind = iota
	// ToolsComponent supplies the applied tool instances and published definitions.
	ToolsComponent
	// PromptComponent supplies the first prompt and published tool definitions.
	PromptComponent
	// ModelComponent restores retained model configuration at its owner.
	ModelComponent
	// AgentComponent supplies an initialized execution owner.
	AgentComponent
	// CommandsComponent supplies the optional command endpoint.
	CommandsComponent
	// AdditionalComponent supplies a declared extension capability.
	AdditionalComponent
)

// Component identifies a readiness requirement. Index addresses an additional
// capability in declaration order; standard capabilities use zero.
type Component struct {
	Kind  ComponentKind
	Index int
}

// Requirements describes the capabilities required for Agent availability.
type Requirements struct {
	Commands   bool
	Components int
}

// BeginInitialization fixes readiness requirements independently of requests.
func (directory *Registry) BeginInitialization(identifier session.SessionID, required Requirements) error {
	err := directory.CheckAdmission(identifier)
	if err != nil {
		return err
	}
	entry := directory.entries[identifier]
	if entry.lifecycle != Initializing || entry.requirements != nil || required.Components < 0 {
		return errors.New("agent: invalid initialization requirements")
	}
	entry.requirements = componentRequirements(required)
	entry.ready = make(map[Component]struct{})
	directory.entries[identifier] = entry
	return nil
}

// NextComponent selects an unmet dependency from confirmed readiness facts.
// There is no mutable startup cursor: failed or unconfirmed requests never make
// a component ready, and closing admission prevents further initialization.
func (directory *Registry) NextComponent(identifier session.SessionID) (Component, bool, error) {
	err := directory.CheckAdmission(identifier)
	if err != nil {
		return Component{}, false, err
	}
	entry := directory.entries[identifier]
	if entry.requirements == nil {
		return Component{}, false, errors.New("agent: initialization requirements are missing")
	}
	for _, dependency := range entry.requirements {
		_, ready := entry.ready[dependency.component]
		if !ready && dependency.satisfied(entry.ready) {
			return dependency.component, true, nil
		}
	}

	if len(entry.ready) != len(entry.requirements) {
		return Component{}, false, errors.New("agent: required dependencies cannot become ready")
	}
	return Component{}, false, nil
}

// ConfirmComponent records readiness only for the expected dependency. A result
// received after close started still leaves its resource responsibility intact.
func (directory *Registry) ConfirmComponent(identifier session.SessionID, dependency Component) error {
	err := directory.checkComponent(identifier, dependency)
	if err != nil {
		return err
	}

	directory.entries[identifier].ready[dependency] = struct{}{}
	return nil
}

// AcquireComponent records the selected component's responsibility before delivery.
// The mapping describes ownership; it does not encode transport request stages.
func (directory *Registry) AcquireComponent(identifier session.SessionID, dependency Component) error {
	err := directory.checkComponent(identifier, dependency)
	if err != nil {
		return err
	}

	kind, owns := componentResources[dependency.Kind]
	if owns {
		return directory.TrackResource(identifier, Resource{
			Kind:  kind,
			Index: dependency.Index,
		})
	}
	return nil
}

// RejectComponent releases only a confirmed rejected acquisition.
func (directory *Registry) RejectComponent(identifier session.SessionID, dependency Component) {
	kind, owns := componentResources[dependency.Kind]
	if owns {
		delete(directory.entries[identifier].resources, Resource{
			Kind:  kind,
			Index: dependency.Index,
		})
	}
}

var componentResources = map[ComponentKind]ResourceKind{
	SessionComponent:    Conversation,
	ToolsComponent:      ToolsRuntime,
	PromptComponent:     PromptRuntime,
	AgentComponent:      Activation,
	CommandsComponent:   CommandRuntime,
	AdditionalComponent: Capability,
}

type requirement struct {
	component    Component
	dependencies []Component
}

func (dependency requirement) satisfied(ready map[Component]struct{}) bool {
	for _, prerequisite := range dependency.dependencies {
		_, found := ready[prerequisite]
		if !found {
			return false
		}
	}
	return true
}

func (directory *Registry) checkComponent(identifier session.SessionID, dependency Component) error {
	err := directory.CheckAdmission(identifier)
	if err != nil {
		return err
	}
	entry := directory.entries[identifier]
	_, ready := entry.ready[dependency]
	if ready {
		return errors.New("agent: component is already ready")
	}
	for _, required := range entry.requirements {
		if required.component == dependency && required.satisfied(entry.ready) {
			return nil
		}
	}
	return errors.New("agent: component dependencies are not ready")
}

func componentRequirements(required Requirements) []requirement {
	conversation := Component{Kind: SessionComponent}
	tools := Component{Kind: ToolsComponent}
	prompt := Component{Kind: PromptComponent}
	models := Component{Kind: ModelComponent}
	executor := Component{Kind: AgentComponent}
	commands := Component{Kind: CommandsComponent}
	result := []requirement{
		{component: conversation},
		{
			component:    tools,
			dependencies: []Component{conversation},
		},
		{
			component:    prompt,
			dependencies: []Component{tools},
		},
		{
			component:    models,
			dependencies: []Component{conversation},
		},
		{
			component:    executor,
			dependencies: []Component{prompt, models},
		},
	}
	previous := executor
	if required.Commands {
		result = append(result, requirement{
			component:    commands,
			dependencies: []Component{executor},
		})
		previous = commands
	}
	for index := 0; index < required.Components; index++ {
		extension := Component{
			Kind:  AdditionalComponent,
			Index: index,
		}
		result = append(result, requirement{
			component:    extension,
			dependencies: []Component{previous},
		})
		previous = extension
	}
	return result
}
