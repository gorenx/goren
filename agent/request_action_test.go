package agent_test

import (
	"context"

	agentcore "github.com/gorenx/goren/agent"
)

type requestResolutionAction struct {
	resolution agentcore.RequestResolution
	order      *[]string
}

func (action requestResolutionAction) Execute(
	context.Context,
	agentcore.RequestNotice,
) (agentcore.RequestResolution, error) {
	if action.order != nil {
		*action.order = append(*action.order, "terminal")
	}
	return action.resolution, nil
}
