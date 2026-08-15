package assembly

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/llm"
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

func (instance *sessionTitlePlugin) Manifest() plugin.Manifest {
	requires := []plugin.ServiceRef{session.StoreService.Ref(), sessionprojection.Service.Ref()}
	if instance.settings.LLM != nil {
		requires = append(requires, llm.Service.Ref())
	}
	return plugin.Manifest{
		Name:     SessionTitleFactoryName,
		Provides: []plugin.ServiceRef{sessiontitle.Service.Ref()},
		Requires: requires,
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
	if instance.settings.LLM != nil {
		modelRuntime, found := plugin.Require(pluginScope, llm.Service)
		if !found {
			return errors.New("assembly: Session Title LLM runtime is unavailable")
		}
		implementation, providerErr := sessiontitle.NewConfiguredLLMProvider(modelRuntime, *instance.settings.LLM)
		if providerErr != nil {
			return providerErr
		}
		if _, providerErr = titles.Register(pluginScope, implementation); providerErr != nil {
			return providerErr
		}
	}
	_, err = plugin.Provide(pluginScope, sessiontitle.Service, sessiontitle.TitleService(titles))
	return err
}
