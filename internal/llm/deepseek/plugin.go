package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/credentials"
	"github.com/gorenx/goren/internal/llm/deepseek/anonymoususerid"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
)

// PluginName is the canonical Harness DeepSeek Provider Plugin name.
const PluginName = "@deepseek-ai/dsh-llm-deepseek"

// HostEnvironment supplies the process-derived values required by DeepSeek
// configuration and the anonymous Harness installation identity.
type HostEnvironment interface {
	LaunchEnvironment
	UserHomeDir() (string, error)
}

// Factory owns strict DeepSeek configuration and Plugin construction.
type Factory struct {
	hostEnvironment HostEnvironment
}

// NewFactory constructs the statically linked DeepSeek Provider Factory.
func NewFactory(processHost HostEnvironment) (*Factory, error) {
	if processHost == nil {
		return nil, errors.New("llm-deepseek: host environment is required")
	}
	return &Factory{
		hostEnvironment: processHost,
	}, nil
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return PluginName
}

// Create strictly decodes configuration and constructs the Provider Plugin.
func (builder *Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := pluginfactory.ValidateCreateContext(createContext); err != nil {
		return nil, err
	}
	if err := pluginfactory.ValidateObjectConfig(
		rawConfig,
		"llm-deepseek",
	); err != nil {
		return nil, err
	}
	var settings Config
	if err := json.Unmarshal(rawConfig, &settings); err != nil {
		return nil, fmt.Errorf("llm-deepseek: decode configuration: %w", err)
	}
	connection, err := ResolveOptions(settings, builder.hostEnvironment)
	if err != nil {
		return nil, err
	}
	return &Plugin{
		connection:      connection.Snapshot(),
		hostEnvironment: builder.hostEnvironment,
	}, nil
}

// Plugin owns the DeepSeek Adapter and its LLM directory and route
// registrations.
type Plugin struct {
	plugin.Base
	connection      ConnectionOptions
	hostEnvironment HostEnvironment

	models             llm.LlmRuntime
	credentialProvider credentials.Provider
	identity           *identityProvider

	directoryRegistration llm.DirectoryRegistrationHandle
	adapterRegistration   llm.AdapterRegistrationHandle
}

// Manifest declares the LLM and Credentials dependencies.
func (*Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[llm.LlmRuntime](),
			plugin.ServiceOf[credentials.Provider](),
		},
	}
}

// Apply constructs the Adapter and installs its owned LLM registrations.
func (instance *Plugin) Apply(requestContext context.Context) error {
	models, err := plugin.Require[llm.LlmRuntime](instance)
	if err != nil {
		return err
	}
	credentialProvider, err := plugin.Require[credentials.Provider](instance)
	if err != nil {
		return err
	}
	instance.models = models
	instance.credentialProvider = credentialProvider
	instance.identity = &identityProvider{
		hostEnvironment: instance.hostEnvironment,
	}
	backend, err := NewAdapter(AdapterDependencies{
		Connections: instance,
		Credentials: instance,
		Identity:    instance.identity,
	})
	if err != nil {
		return err
	}
	directoryRegistration, err := models.RegisterConfigurableProviders(
		requestContext,
		[]llm.ConfigurableProvider{
			{
				Provider:     ProviderRoute,
				DisplayName:  "DeepSeek",
				SettingsNS:   SettingsNamespace,
				SettingsPath: []string{},
			},
		},
	)
	if err != nil {
		return err
	}
	instance.directoryRegistration = directoryRegistration
	adapterRegistration, err := models.RegisterAdapter(
		requestContext,
		[]string{ProviderRoute},
		backend,
	)
	if err != nil {
		return err
	}
	instance.adapterRegistration = adapterRegistration
	return nil
}

// Dispose unregisters Adapter routes and directory entries in reverse order.
func (instance *Plugin) Dispose(closeContext context.Context) error {
	var disposeErr error
	if instance.adapterRegistration != nil {
		disposeErr = errors.Join(
			disposeErr,
			instance.adapterRegistration.Release(closeContext),
		)
		instance.adapterRegistration = nil
	}
	if instance.directoryRegistration != nil {
		disposeErr = errors.Join(
			disposeErr,
			instance.directoryRegistration.Release(closeContext),
		)
		instance.directoryRegistration = nil
	}
	instance.models = nil
	instance.credentialProvider = nil
	instance.identity = nil
	return disposeErr
}

// CurrentConnection returns a detached request-generation snapshot.
func (instance *Plugin) CurrentConnection() (ConnectionOptions, error) {
	return instance.connection.Snapshot(), nil
}

// ResolveAPIKey resolves the configured credential for one new request.
func (instance *Plugin) ResolveAPIKey(
	requestContext context.Context,
	connection ConnectionOptions,
) (string, error) {
	credentialRef, err := credentials.NewRef(connection.APIKeyEnv)
	if err != nil {
		return "", err
	}
	resolved, found, err := instance.credentialProvider.Resolve(requestContext, credentialRef)
	if err != nil {
		return "", err
	}
	if found {
		return resolved.Value, nil
	}
	return "", llm.MustLlmError(
		fmt.Sprintf(
			"llm-deepseek: no API key for provider route %q; configure %s in Web Settings or export it in the launching environment",
			ProviderRoute,
			connection.APIKeyEnv,
		),
		"MISSING_CREDENTIAL",
	)
}

type identityProvider struct {
	hostEnvironment HostEnvironment

	mu      sync.Mutex
	storage *anonymoususerid.Store
}

func (provider *identityProvider) UserID() (string, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.storage == nil {
		storage, err := anonymoususerid.New(provider.hostEnvironment)
		if err != nil {
			return "", err
		}
		provider.storage = storage
	}
	return provider.storage.Value()
}

var _ pluginfactory.Factory = (*Factory)(nil)
