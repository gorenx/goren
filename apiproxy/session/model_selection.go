package sessionapi

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
)

const modelSelectionPluginName = "@deepseek-ai/dsh-host-apiproxy/session-model-selection"

// selectionExtension binds API-facing selection state to one exact Agent tree.
// Its child owns the prompt/request Waterfalls; this Plugin owns map membership.
type selectionExtension struct {
	plugin.Base
	owner      *AgentSessions
	selection  *agent.ModelSelectionRef
	middleware *agent.ModelSelectionPlugin
	subject    agent.Agent
}

func newSelectionExtension(
	owner *AgentSessions,
	selectionRef *agent.ModelSelectionRef,
) (*selectionExtension, error) {
	if owner == nil || selectionRef == nil {
		return nil, errors.New(
			"apiproxy/session: model selection owner and reference are required",
		)
	}
	middleware, err := agent.NewModelSelectionPlugin(selectionRef)
	if err != nil {
		return nil, err
	}
	return &selectionExtension{
		owner:      owner,
		selection:  selectionRef,
		middleware: middleware,
	}, nil
}

func (extension *selectionExtension) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: modelSelectionPluginName,
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[agent.Agent](),
		},
		Children: []plugin.ChildPlugin{
			{
				Instance:  extension.middleware,
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationMain,
			},
		},
	}
}

func (extension *selectionExtension) Apply(requestContext context.Context) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	subject, err := plugin.Require[agent.Agent](extension)
	if err != nil {
		return err
	}
	if err = extension.owner.installSelection(subject, extension.selection); err != nil {
		return err
	}
	extension.subject = subject
	return nil
}

func (extension *selectionExtension) Dispose(context.Context) error {
	extension.owner.removeSelection(extension.subject, extension.selection)
	extension.subject = nil
	return nil
}
