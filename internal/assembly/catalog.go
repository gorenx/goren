// Package assembly owns the statically linked plugin catalog and shipped
// server composition. It contains only capabilities included in the current port.
package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	credentialslocal "github.com/gorenx/goren/credentials/local"
	"github.com/gorenx/goren/internal/llm/deepseek"
	"github.com/gorenx/goren/plugin"
	sesspersist "github.com/gorenx/goren/session/persistence"
	querysqlite "github.com/gorenx/goren/session/query/sqlite"
	sessiontitle "github.com/gorenx/goren/session/title"
)

const (
	AgentFactoryName              = "@deepseek-ai/dsh-agent"
	AgentDefaultModelFactoryName  = "@deepseek-ai/dsh-agent-default-model"
	AgentLoopFactoryName          = "@deepseek-ai/dsh-agent-loop"
	ApprovalFactoryName           = "@deepseek-ai/dsh-user-approval"
	APIProxyFactoryName           = "@deepseek-ai/dsh-host-apiproxy"
	ConnectionFactoryName         = "@deepseek-ai/dsh-client-connection"
	CredentialsFactoryName        = "@deepseek-ai/dsh-credentials"
	DeepSeekFactoryName           = "@deepseek-ai/dsh-llm-deepseek"
	LLMFactoryName                = "@deepseek-ai/dsh-llm"
	LLMRetryFactoryName           = "@deepseek-ai/dsh-llm-retry"
	SessionFactoryName            = "@deepseek-ai/dsh-session"
	SessionPersistenceFactoryName = "@deepseek-ai/dsh-session-persistence"
	SessionProjectionFactoryName  = "@deepseek-ai/dsh-session-projection"
	SessionQueryFactoryName       = "@deepseek-ai/dsh-session-query"
	SessionTitleFactoryName       = "@deepseek-ai/dsh-session-title"
	SystemPromptFactoryName       = "@deepseek-ai/dsh-system-prompt"
	ToolAskUserFactoryName        = "@deepseek-ai/dsh-tool-ask-user"
	ToolsFactoryName              = "@deepseek-ai/dsh-tools"
	UserQuestionsFactoryName      = "@deepseek-ai/dsh-user-questions"
	WebFrontendFactoryName        = "@gorenx/dsh-web"
	WorkspaceFactoryName          = "@deepseek-ai/dsh-workspace"
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
	if err := plugin.RegisterFactory(registry, approvalFactory{}); err != nil {
		return nil, err
	}
	ensureDirectory := platform.EnsureDirectory
	if ensureDirectory == nil {
		ensureDirectory = func(path string) error { return os.MkdirAll(path, 0o755) }
	}
	if err := plugin.RegisterFactory(registry,
		apiProxyFactory{
			workingDirectory: platform.WorkingDirectory,
			ensureDirectory:  ensureDirectory,
		},
	); err != nil {
		return nil, err
	}
	if err := plugin.RegisterFactory(registry, connectionFactory{}); err != nil {
		return nil, err
	}
	lookupEnv := platform.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	if err := plugin.RegisterFactory(registry, credentialsFactory{lookupEnv: lookupEnv}); err != nil {
		return nil, err
	}
	if err := plugin.RegisterFactory(registry, llmFactory{}); err != nil {
		return nil, err
	}
	userHome := platform.UserHomeDir
	if userHome == nil {
		userHome = os.UserHomeDir
	}
	if err := plugin.RegisterFactory(registry, deepSeekFactory{lookupEnv: lookupEnv, userHome: userHome}); err != nil {
		return nil, err
	}
	if err := plugin.RegisterFactory(registry, llmRetryFactory{}); err != nil {
		return nil, err
	}
	if err := plugin.RegisterFactory(registry, sessionFactory{}); err != nil {
		return nil, err
	}
	if err := plugin.RegisterFactory(registry, sessionPersistenceFactory{}); err != nil {
		return nil, err
	}
	if err := plugin.RegisterFactory(registry, sessionProjectionFactory{}); err != nil {
		return nil, err
	}
	if err := plugin.RegisterFactory(registry, sessionQueryFactory{}); err != nil {
		return nil, err
	}
	if err := plugin.RegisterFactory(registry, sessionTitleFactory{}); err != nil {
		return nil, err
	}
	if err := plugin.RegisterFactory(registry, systemPromptFactory{}); err != nil {
		return nil, err
	}
	if err := plugin.RegisterFactory(registry, toolsFactory{}); err != nil {
		return nil, err
	}
	if err := plugin.RegisterFactory(registry, toolAskUserFactory{}); err != nil {
		return nil, err
	}
	if err := plugin.RegisterFactory(registry, userQuestionsFactory{}); err != nil {
		return nil, err
	}
	if err := plugin.RegisterFactory(registry, webFrontendFactory{}); err != nil {
		return nil, err
	}
	if err := plugin.RegisterFactory(registry, workspaceFactory{}); err != nil {
		return nil, err
	}
	return registry, nil
}

// DefaultSpecs builds the current server composition. Consumers are
// intentionally declared before Session to exercise dependency settlement
// instead of relying on file order.
func DefaultSpecs(
	listenAddress string,
	version string,
	sessionDatabasePath string,
	workspaceDatabasePath string,
) ([]PluginSpec, error) {
	connectionRaw, err := json.Marshal(ConnectionConfig{ListenAddress: listenAddress, ServeWeb: true})
	if err != nil {
		return nil, err
	}
	apiProxyRaw, err := json.Marshal(APIProxyConfig{Version: version})
	if err != nil {
		return nil, err
	}
	defaultModelRaw, err := json.Marshal(AgentDefaultModelConfig{
		Provider: deepseek.ProviderRoute, Model: deepseek.DefaultModelID,
	})
	if err != nil {
		return nil, err
	}
	persistenceRaw, err := json.Marshal(SessionPersistenceConfig{
		Path: sessionDatabasePath, JournalMode: "wal",
		WriteBatchMaxDelayMS:     sesspersist.DefaultWriteBatchMaxDelay.Milliseconds(),
		PreparedSessionCacheSize: sesspersist.DefaultPreparedSessionCache,
	})
	if err != nil {
		return nil, err
	}
	queryRaw, err := json.Marshal(SessionQueryConfig{
		Path:        filepath.Join(filepath.Dir(sessionDatabasePath), "session-query.sqlite"),
		JournalMode: querysqlite.JournalWAL,
	})
	if err != nil {
		return nil, err
	}
	credentialsRaw, err := json.Marshal(CredentialsConfig{
		Local: credentialslocal.Config{Path: filepath.Join(filepath.Dir(sessionDatabasePath), ".credentials.json")},
	})
	if err != nil {
		return nil, err
	}
	workspaceRaw, err := json.Marshal(WorkspaceConfig{
		Path: workspaceDatabasePath, JournalMode: "wal",
	})
	if err != nil {
		return nil, err
	}
	titleRaw, err := json.Marshal(
		sessiontitle.Config{
			FallbackMaxWords: 5,
			FallbackMaxBytes: 40,
			MaxTitleBytes:    80,
			LLM: &sessiontitle.LLMConfig{
				AutomaticMode:       sessiontitle.AutomaticFirstPrompt,
				TargetWords:         5,
				TargetCJKCharacters: 10,
				MaxInputBytes:       4096,
				MaxOutputTokens:     64,
				TimeoutMS:           60000,
			},
		})
	if err != nil {
		return nil, err
	}
	return []PluginSpec{
		{FactoryName: ConnectionFactoryName, Config: connectionRaw},
		{FactoryName: APIProxyFactoryName, Config: apiProxyRaw},
		{FactoryName: ToolAskUserFactoryName, Config: json.RawMessage(`{}`)},
		{FactoryName: AgentDefaultModelFactoryName, Config: defaultModelRaw},
		{FactoryName: LLMRetryFactoryName, Config: json.RawMessage(`{}`)},
		{FactoryName: SessionTitleFactoryName, Config: titleRaw},
		{FactoryName: SessionProjectionFactoryName, Config: json.RawMessage(`{}`)},
		{FactoryName: AgentLoopFactoryName, Config: json.RawMessage(`{}`)},
		{FactoryName: ApprovalFactoryName, Config: json.RawMessage(`{}`)},
		{FactoryName: UserQuestionsFactoryName, Config: json.RawMessage(`{}`)},
		{FactoryName: AgentFactoryName, Config: json.RawMessage(`{}`)},
		{FactoryName: LLMFactoryName, Config: json.RawMessage(`{}`)},
		{FactoryName: DeepSeekFactoryName, Config: json.RawMessage(`{}`)},
		{FactoryName: CredentialsFactoryName, Config: credentialsRaw},
		{FactoryName: SystemPromptFactoryName, Config: json.RawMessage(`{}`)},
		{FactoryName: ToolsFactoryName, Config: json.RawMessage(`{}`)},
		{FactoryName: SessionFactoryName, Config: json.RawMessage(`{}`)},
		{FactoryName: SessionPersistenceFactoryName, Config: persistenceRaw},
		{FactoryName: SessionQueryFactoryName, Config: queryRaw},
		{FactoryName: WorkspaceFactoryName, Config: workspaceRaw},
		{FactoryName: WebFrontendFactoryName, Config: json.RawMessage(`{}`)},
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
