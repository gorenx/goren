package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/gorenx/goren/credentials"
	"github.com/gorenx/goren/internal/anonymoususerid"
	"github.com/gorenx/goren/internal/llmdeepseek"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
)

type deepSeekFactory struct {
	lookupEnv func(string) (string, bool)
	userHome  func() (string, error)
}

func (deepSeekFactory) Name() string { return DeepSeekFactoryName }

func (factory deepSeekFactory) DecodeConfig(rawConfig json.RawMessage) (llmdeepseek.ConnectionOptions, error) {
	settings, err := plugin.DecodeStrictConfig[llmdeepseek.Config](rawConfig, nil)
	if err != nil {
		return llmdeepseek.ConnectionOptions{}, err
	}
	return llmdeepseek.ResolveOptions(settings, llmdeepseek.Environment{LookupEnv: factory.lookupEnv})
}

func (factory deepSeekFactory) New(_ context.Context, connection llmdeepseek.ConnectionOptions) (plugin.Plugin, error) {
	return &deepSeekPlugin{
		connection: connection.Snapshot(), lookupEnv: factory.lookupEnv, userHome: factory.userHome,
	}, nil
}

type deepSeekPlugin struct {
	connection llmdeepseek.ConnectionOptions
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
	backend, err := llmdeepseek.NewAdapter(llmdeepseek.AdapterOptions{
		CurrentOptions: func() (llmdeepseek.ConnectionOptions, error) {
			return instance.connection.Snapshot(), nil
		},
		ResolveAPIKey: func(resolveContext context.Context, connection llmdeepseek.ConnectionOptions) (string, error) {
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
					llmdeepseek.ProviderRoute, connection.APIKeyEnv,
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
		Provider: llmdeepseek.ProviderRoute, DisplayName: "DeepSeek",
		SettingsNS: llmdeepseek.SettingsNamespace, SettingsPath: []string{},
	}}); err != nil {
		return err
	}
	_, err = llmService.RegisterAdapter(requestContext, pluginScope, []string{llmdeepseek.ProviderRoute}, backend)
	return err
}
