package control

import (
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/tools"
)

// controlTools adapts Subagent control and directory capabilities into the three
// model-facing control Tool definitions. It has no Plugin lifecycle.
type controlTools struct {
	children  subagent.ChildControl
	directory subagent.ChildDirectory
	agents    agent.Registry
}

func newControlTools(
	children subagent.ChildControl,
	directory subagent.ChildDirectory,
	agentRegistry agent.Registry,
) (*controlTools, error) {
	if children == nil || directory == nil || agentRegistry == nil {
		return nil, errors.New(
			"subagent control tools require ChildControl, ChildDirectory, and Agent Registry",
		)
	}
	return &controlTools{
		children:  children,
		directory: directory,
		agents:    agentRegistry,
	}, nil
}

func (adapter *controlTools) definitions() []tools.ToolDefinition {
	return []tools.ToolDefinition{
		adapter.sendMessageDefinition(),
		adapter.interruptDefinition(),
		adapter.listDefinition(),
	}
}
