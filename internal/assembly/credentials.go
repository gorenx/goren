package assembly

import (
	"context"
	"encoding/json"
	"os"

	"github.com/gorenx/goren/credentials"
	credentialslocal "github.com/gorenx/goren/credentials/local"
	"github.com/gorenx/goren/plugin"
)

// CredentialsConfig selects the storage adapter used by the Credentials plugin.
type CredentialsConfig struct {
	Local credentialslocal.Config `json:"local"`
}

type credentialsFactory struct {
	lookupEnv func(string) (string, bool)
}

func (credentialsFactory) Name() string { return CredentialsFactoryName }

func (factory credentialsFactory) DecodeConfig(rawConfig json.RawMessage) (CredentialsConfig, error) {
	return plugin.DecodeStrictConfig(rawConfig, func(settings CredentialsConfig) error {
		return credentialslocal.ValidateConfig(settings.Local)
	})
}

func (factory credentialsFactory) New(_ context.Context, settings CredentialsConfig) (plugin.Plugin, error) {
	storage, err := credentialslocal.NewStore(settings.Local)
	if err != nil {
		return nil, err
	}
	lookup := factory.lookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	credentialManager, err := credentials.NewManager(storage, credentials.Environment{LookupEnv: lookup})
	if err != nil {
		return nil, err
	}
	return &credentialsPlugin{manager: credentialManager}, nil
}

type credentialsPlugin struct {
	manager *credentials.Manager
}

func (*credentialsPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: CredentialsFactoryName, Provides: []plugin.ServiceRef{credentials.Service.Ref()}}
}

func (instance *credentialsPlugin) Apply(_ context.Context, pluginScope *plugin.Scope) error {
	_, err := plugin.Provide(pluginScope, credentials.Service, credentials.Provider(instance.manager))
	return err
}
