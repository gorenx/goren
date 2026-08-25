package userquestions

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
)

// Plugin adapts the User Questions business Service to Plugin Runtime. It
// owns Service publication and dependency resolution, not question state.
type Plugin struct {
	plugin.Base
	service *QuestionService
}

// NewPlugin constructs the canonical User Questions Plugin and its business
// Service.
func NewPlugin() *Plugin {
	return &Plugin{
		service: newQuestionService(),
	}
}

// Manifest publishes UserQuestions and optionally consumes the Agent Registry
// used by the business Service to attest Agent-backed requests.
func (owner *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[UserQuestions](owner.service),
		},
		Optional: []plugin.ServiceType{
			plugin.ServiceOf[agent.Registry](),
		},
	}
}

// Apply resolves Plugin dependencies and activates the business Service before
// Runtime publishes its capability binding.
func (owner *Plugin) Apply(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("userquestions: Plugin Apply Context is nil")
	}
	if err := requestContext.Err(); err != nil {
		return err
	}
	agents, _ := plugin.Resolve[agent.Registry](owner)
	return owner.service.activate(agents)
}

// Dispose closes the business Service after dependent Plugins have stopped.
func (owner *Plugin) Dispose(context.Context) error {
	owner.service.close()
	return nil
}

var _ plugin.Plugin = (*Plugin)(nil)
