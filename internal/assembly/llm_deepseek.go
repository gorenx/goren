package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/gorenx/goren/credentials"
	"github.com/gorenx/goren/internal/llm/deepseek"
	"github.com/gorenx/goren/internal/llm/deepseek/anonymoususerid"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
)

type deepSeekFactory struct {
	lookupEnv func(string) (string, bool)
	userHome  func() (string, error)
}

func (deepSeekFactory) Name() string { return DeepSeekFactoryName }

func (factory deepSeekFactory) DecodeConfig(rawConfig json.RawMessage) (deepseek.ConnectionOptions, error) {
	settings, err := plugin.DecodeStrictConfig[deepseek.Config](rawConfig, nil)
	if err != nil {
		return deepseek.ConnectionOptions{}, err
	}
	return deepseek.ResolveOptions(settings, deepseek.Environment{LookupEnv: factory.lookupEnv})
}

func (factory deepSeekFactory) New(_ context.Context, connection deepseek.ConnectionOptions) (plugin.Plugin, error) {
	return &deepSeekPlugin{
		connection: connection.Snapshot(), lookupEnv: factory.lookupEnv, userHome: factory.userHome,
	}, nil
}

type deepSeekPlugin struct {
	connection deepseek.ConnectionOptions
	lookupEnv  func(string) (string, bool)
	userHome   func() (string, error)
}

func (*deepSeekPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:     DeepSeekFactoryName,
		Requires: []plugin.ServiceRef{llm.Service.Ref(), credentials.Service.Ref()},
	}
}

func (instance *deepSeekPlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	llmService, found := plugin.Require(pluginScope, llm.Service)
	credentialProvider, credentialsFound := plugin.Require(pluginScope, credentials.Service)
	if !found || !credentialsFound {
		return errors.New("assembly: required llm or credentials service is unavailable")
	}
	lookupEnv := instance.lookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	var identityMu sync.Mutex
	var identityStore *anonymoususerid.Store
	resolveIdentity := func() (string, error) {
		identityMu.Lock()
		defer identityMu.Unlock()
		if identityStore == nil {
			created, err := anonymoususerid.New(lookupEnv, instance.userHome)
			if err != nil {
				return "", err
			}
			identityStore = created
		}
		return identityStore.Value()
	}
	backend, err := deepseek.NewAdapter(deepseek.AdapterOptions{
		CurrentOptions: func() (deepseek.ConnectionOptions, error) {
			return instance.connection.Snapshot(), nil
		},
		ResolveAPIKey: func(resolveContext context.Context, connection deepseek.ConnectionOptions) (string, error) {
			ref, refErr := credentials.NewRef(connection.APIKeyEnv)
			if refErr != nil {
				return "", refErr
			}
			resolved, credentialFound, resolveErr := credentialProvider.Resolve(resolveContext, ref)
			if resolveErr != nil {
				return "", resolveErr
			}
			if credentialFound {
				return resolved.Value, nil
			}
			return "", llm.MustLlmError(
				fmt.Sprintf(
					"llm-deepseek: no API key for provider route %q; configure %s in Web Settings or export it in the launching environment",
					deepseek.ProviderRoute, connection.APIKeyEnv,
				),
				"MISSING_CREDENTIAL",
			)
		},
		ResolveUserID: resolveIdentity,
	})
	if err != nil {
		return err
	}
	if _, err := llmService.RegisterConfigurableProviders(requestContext, pluginScope, []llm.ConfigurableProvider{{
		Provider: deepseek.ProviderRoute, DisplayName: "DeepSeek",
		SettingsNS: deepseek.SettingsNamespace, SettingsPath: []string{},
	}}); err != nil {
		return err
	}
	_, err = llmService.RegisterAdapter(requestContext, pluginScope, []string{deepseek.ProviderRoute}, backend)
	return err
}
