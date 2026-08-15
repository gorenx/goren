package assembly

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/plugin"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

// SessionProjectionConfig is empty because projection persistence is a
// separate cache adapter capability, not a Registry mode.
type SessionProjectionConfig struct{}

type sessionProjectionFactory struct{}

func (sessionProjectionFactory) Name() string { return SessionProjectionFactoryName }

func (sessionProjectionFactory) DecodeConfig(rawConfig json.RawMessage) (SessionProjectionConfig, error) {
	return plugin.DecodeStrictConfig[SessionProjectionConfig](rawConfig, nil)
}

func (sessionProjectionFactory) New(context.Context, SessionProjectionConfig) (plugin.Plugin, error) {
	return &sessionProjectionPlugin{}, nil
}

type sessionProjectionPlugin struct{}

func (*sessionProjectionPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: SessionProjectionFactoryName, Provides: []plugin.ServiceRef{sessionprojection.Service.Ref()},
	}
}

func (*sessionProjectionPlugin) Apply(_ context.Context, pluginScope *plugin.Scope) error {
	registry, err := sessionprojection.NewDriveRegistry(pluginScope)
	if err != nil {
		return err
	}
	_, err = plugin.Provide(pluginScope, sessionprojection.Service, sessionprojection.Registry(registry))
	return err
}
