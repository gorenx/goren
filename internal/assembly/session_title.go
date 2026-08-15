package assembly

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
	sessiontitle "github.com/gorenx/goren/session/title"
)

type sessionTitleFactory struct{}

func (sessionTitleFactory) Name() string { return SessionTitleFactoryName }

func (sessionTitleFactory) DecodeConfig(rawConfig json.RawMessage) (sessiontitle.Config, error) {
	return plugin.DecodeStrictConfig(rawConfig, func(settings sessiontitle.Config) error {
		_, err := settings.Validate()
		return err
	})
}

func (sessionTitleFactory) New(_ context.Context, settings sessiontitle.Config) (plugin.Plugin, error) {
	return &sessionTitlePlugin{settings: settings}, nil
}

type sessionTitlePlugin struct {
	settings sessiontitle.Config
}

func (*sessionTitlePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:     SessionTitleFactoryName,
		Provides: []plugin.ServiceRef{sessiontitle.Service.Ref()},
		Requires: []plugin.ServiceRef{session.StoreService.Ref(), sessionprojection.Service.Ref()},
	}
}

func (instance *sessionTitlePlugin) Apply(_ context.Context, pluginScope *plugin.Scope) error {
	store, storeFound := plugin.Require(pluginScope, session.StoreService)
	projections, projectionsFound := plugin.Require(pluginScope, sessionprojection.Service)
	if !storeFound || !projectionsFound {
		return errors.New("assembly: Session Title dependencies are unavailable")
	}
	titles, err := sessiontitle.NewLogService(pluginScope, store, projections, instance.settings, sessiontitle.Options{})
	if err != nil {
		return err
	}
	_, err = plugin.Provide(pluginScope, sessiontitle.Service, sessiontitle.TitleService(titles))
	return err
}
