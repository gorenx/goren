package control

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/tools"
)

// controlTools adapts Subagent control and directory capabilities into the three
// model-facing control Tool definitions. It has no Plugin lifecycle.
type controlTools struct {
	children  subagent.ChildControl
	directory subagent.ChildDirectory
	activity  activityReader
}

type activityReader interface {
	Status(context.Context, session.SessionID) (agent.Status, bool, error)
}

func newControlTools(
	children subagent.ChildControl,
	directory subagent.ChildDirectory,
	activity activityReader,
) (*controlTools, error) {
	if children == nil || directory == nil || activity == nil {
		return nil, errors.New(
			"subagent control tools require ChildControl, ChildDirectory, and activity reader",
		)
	}
	return &controlTools{
		children:  children,
		directory: directory,
		activity:  activity,
	}, nil
}

func (adapter *controlTools) definitions() []tools.ToolDefinition {
	return []tools.ToolDefinition{
		adapter.sendMessageDefinition(),
		adapter.interruptDefinition(),
		adapter.listDefinition(),
	}
}
