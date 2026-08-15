// Package assembly owns the statically linked plugin catalog and shipped
// server composition. It contains only capabilities included in the current port.
package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/gorenx/goren/plugin"
)

const (
	AgentFactoryName             = "@deepseek-ai/dsh-agent"
	AgentDefaultModelFactoryName = "@deepseek-ai/dsh-agent-default-model"
	AgentLoopFactoryName         = "@deepseek-ai/dsh-agent-loop"
	APIProxyFactoryName          = "@deepseek-ai/dsh-host-apiproxy"
	ConnectionFactoryName        = "@deepseek-ai/dsh-client-connection"
	DeepSeekFactoryName          = "@deepseek-ai/dsh-llm-deepseek"
	LLMFactoryName               = "@deepseek-ai/dsh-llm"
	SessionFactoryName           = "@deepseek-ai/dsh-session"
	SystemPromptFactoryName      = "@deepseek-ai/dsh-system-prompt"
	ToolsFactoryName             = "@deepseek-ai/dsh-tools"
)

// Environment contains process-derived values that are not deployment config.
type Environment struct {
	WorkingDirectory string
	LookupEnv        func(string) (string, bool)
	UserHomeDir      func() (string, error)
	EnsureDirectory  func(string) error
}

// PluginSpec is one strict factory invocation at the catalog ingress boundary.
type PluginSpec struct {
	FactoryName string
	Config      json.RawMessage
}

// NewCatalog registers only the factories included in the current server slice.
func NewCatalog(platform Environment) (*plugin.Catalog, error) {
	registry := plugin.NewCatalog()
	if err := plugin.RegisterFactory(registry, agentFactory{}); err != nil {
		return nil, err
	}
	if err := plugin.RegisterFactory(registry, agentDefaultModelFactory{}); err != nil {
		return nil, err
	}
	if err := plugin.RegisterFactory(registry, agentLoopFactory{}); err != nil {
		return nil, err
	}
	ensureDirectory := platform.EnsureDirectory
	if ensureDirectory == nil {
		ensureDirectory = func(path string) error { return os.MkdirAll(path, 0o755) }
	}
	if err := plugin.RegisterFactory(registry, apiProxyFactory{
		workingDirectory: platform.WorkingDirectory, ensureDirectory: ensureDirectory,
	}); err != nil {
		return nil, err
	}
	if err := plugin.RegisterFactory(registry, connectionFactory{}); err != nil {
		return nil, err
	}
	if err := plugin.RegisterFactory(registry, llmFactory{}); err != nil {
		return nil, err
	}
	lookupEnv := platform.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	userHome := platform.UserHomeDir
	if userHome == nil {
		userHome = os.UserHomeDir
	}
	if err := plugin.RegisterFactory(registry, deepSeekFactory{lookupEnv: lookupEnv, userHome: userHome}); err != nil {
		return nil, err
	}
	if err := plugin.RegisterFactory(registry, sessionFactory{}); err != nil {
		return nil, err
	}
	if err := plugin.RegisterFactory(registry, systemPromptFactory{}); err != nil {
		return nil, err
	}
	if err := plugin.RegisterFactory(registry, toolsFactory{}); err != nil {
		return nil, err
	}
	return registry, nil
}

// DefaultSpecs builds the current server composition. Consumers are
// intentionally declared before Session to exercise dependency settlement
// instead of relying on file order.
func DefaultSpecs(listenAddress string, version string) ([]PluginSpec, error) {
	connectionRaw, err := json.Marshal(ConnectionConfig{ListenAddress: listenAddress})
	if err != nil {
		return nil, err
	}
	apiProxyRaw, err := json.Marshal(APIProxyConfig{Version: version})
	if err != nil {
		return nil, err
	}
	defaultModelRaw, err := json.Marshal(AgentDefaultModelConfig{
		Provider: "deepseek-official", Model: "deepseek-v4-flash",
	})
	if err != nil {
		return nil, err
	}
	return []PluginSpec{
		{FactoryName: ConnectionFactoryName, Config: connectionRaw},
		{FactoryName: APIProxyFactoryName, Config: apiProxyRaw},
		{FactoryName: AgentDefaultModelFactoryName, Config: defaultModelRaw},
		{FactoryName: AgentLoopFactoryName, Config: json.RawMessage(`{}`)},
		{FactoryName: AgentFactoryName, Config: json.RawMessage(`{}`)},
		{FactoryName: LLMFactoryName, Config: json.RawMessage(`{}`)},
		{FactoryName: DeepSeekFactoryName, Config: json.RawMessage(`{}`)},
		{FactoryName: SystemPromptFactoryName, Config: json.RawMessage(`{}`)},
		{FactoryName: ToolsFactoryName, Config: json.RawMessage(`{}`)},
		{FactoryName: SessionFactoryName, Config: json.RawMessage(`{}`)},
	}, nil
}

// Load creates and loads a composition transaction. A failure unloads every
// declaration accepted earlier in the same call, leaving no contributions.
func Load(requestContext context.Context, engine *plugin.Runtime, registry *plugin.Catalog, declarations []PluginSpec) ([]plugin.Handle, error) {
	if engine == nil {
		return nil, errors.New("assembly: plugin runtime is nil")
	}
	if registry == nil {
		return nil, errors.New("assembly: factory catalog is nil")
	}
	handles := make([]plugin.Handle, 0, len(declarations))
	for _, declaration := range declarations {
		instance, err := registry.Create(requestContext, declaration.FactoryName, declaration.Config)
		if err == nil {
			var pluginHandle plugin.Handle
			pluginHandle, err = engine.Load(requestContext, instance)
			if pluginHandle.ID() != 0 {
				handles = append(handles, pluginHandle)
			}
		}
		if err == nil {
			continue
		}
		rollbackErr := unloadReverse(requestContext, engine, handles)
		return nil, errors.Join(fmt.Errorf("assembly: load %s: %w", declaration.FactoryName, err), rollbackErr)
	}
	return handles, nil
}

func unloadReverse(closeContext context.Context, engine *plugin.Runtime, handles []plugin.Handle) error {
	var rollbackErr error
	for index := len(handles) - 1; index >= 0; index-- {
		rollbackErr = errors.Join(rollbackErr, engine.Unload(closeContext, handles[index]))
	}
	return rollbackErr
}
