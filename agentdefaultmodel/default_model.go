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

// DefaultModel exposes the current detached selection independently of API
// transport. SaveSelection is a no-op when no Settings provider is composed.
type DefaultModel interface {
	CurrentSelection() agent.ModelSelection
	SaveSelection(context.Context, agent.ModelSelection) error
}

// Service is the canonical default-model Service Definition.
var Service = plugin.DefineService[DefaultModel](ServiceName)

type staticDefault struct {
	selection agent.ModelSelection
}

// NewStatic constructs the composition-backed provider used until a Settings
// provider supplies a live user layer.
func NewStatic(initial agent.ModelSelection) (DefaultModel, error) {
	if initial.Provider == "" || initial.Model == "" {
		return nil, errors.New("agentdefaultmodel: provider and model are required")
	}
	return &staticDefault{selection: initial}, nil
}

func (defaults *staticDefault) CurrentSelection() agent.ModelSelection {
	return defaults.selection
}

func (*staticDefault) SaveSelection(context.Context, agent.ModelSelection) error {
	// The pinned source also retains the composition entry when its optional
	// Settings provider is absent.
	return nil
}
