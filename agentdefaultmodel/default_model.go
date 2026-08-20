// Package agentdefaultmodel owns the deployment default used when a Session
// has no logged or explicitly selected provider/model route.
package agentdefaultmodel

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
)

const ServiceName = "agentDefaultModel"

// PluginName is the canonical Harness Plugin name.
const PluginName = "@deepseek-ai/dsh-agent-default-model"

// DefaultModel exposes the current detached selection independently of API
// transport. SaveSelection is a no-op when no Settings provider is composed.
type DefaultModel interface {
	plugin.Service
	CurrentSelection() agent.ModelSelection
	SaveSelection(context.Context, agent.ModelSelection) error
}

// StaticPlugin owns the composition-backed default model capability when no
// writable Settings provider is present.
type StaticPlugin struct {
	plugin.Base
	selection agent.ModelSelection
}

// NewStatic constructs the composition-backed provider used until a Settings
// provider supplies a live user layer.
func NewStatic(initial agent.ModelSelection) (*StaticPlugin, error) {
	if initial.Provider == "" || initial.Model == "" {
		return nil, errors.New("agentdefaultmodel: provider and model are required")
	}
	return &StaticPlugin{
		selection: initial,
	}, nil
}

// Manifest provides the canonical default-model Service.
func (*StaticPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Provides: []plugin.ServiceType{
			plugin.ServiceOf[DefaultModel](),
		},
	}
}

// Apply validates startup cancellation before Service publication.
func (*StaticPlugin) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

// Dispose has no process resource to release for the immutable fallback.
func (*StaticPlugin) Dispose(context.Context) error {
	return nil
}

func (defaults *StaticPlugin) CurrentSelection() agent.ModelSelection {
	return defaults.selection
}

func (*StaticPlugin) SaveSelection(context.Context, agent.ModelSelection) error {
	// The pinned source also retains the composition entry when its optional
	// Settings provider is absent.
	return nil
}
