// Package assembly owns process-level Factory registration, deployment
// declarations, and construction of the complete Server Plugin tree.
package assembly

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/gorenx/goren/agent"
	agentfactory "github.com/gorenx/goren/agent/factory"
	"github.com/gorenx/goren/agentdefaultmodel"
	agentdefaultmodelfactory "github.com/gorenx/goren/agentdefaultmodel/factory"
	"github.com/gorenx/goren/agentloop"
	agentloopfactory "github.com/gorenx/goren/agentloop/factory"
	"github.com/gorenx/goren/apiproxy"
	apiproxyfactory "github.com/gorenx/goren/apiproxy/factory"
	apiproxyhost "github.com/gorenx/goren/apiproxy/host"
	"github.com/gorenx/goren/approval"
	approvalfactory "github.com/gorenx/goren/approval/factory"
	"github.com/gorenx/goren/commands"
	commandsfactory "github.com/gorenx/goren/commands/factory"
	"github.com/gorenx/goren/compaction/basic"
	compactionbasicfactory "github.com/gorenx/goren/compaction/basic/factory"
	compactioncommand "github.com/gorenx/goren/compaction/command"
	compactioncommandfactory "github.com/gorenx/goren/compaction/command/factory"
	"github.com/gorenx/goren/compaction/toolresultpruner"
	toolresultprunerfactory "github.com/gorenx/goren/compaction/toolresultpruner/factory"
	"github.com/gorenx/goren/credentials"
	credentialsfactory "github.com/gorenx/goren/credentials/factory"
	credentialslocal "github.com/gorenx/goren/credentials/local"
	connectionhost "github.com/gorenx/goren/internal/connection"
	connectionfactory "github.com/gorenx/goren/internal/connection/factory"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/deepseek"
	llmfactory "github.com/gorenx/goren/llm/factory"
	"github.com/gorenx/goren/llm/retry"
	llmretryfactory "github.com/gorenx/goren/llm/retry/factory"
	"github.com/gorenx/goren/llm/tokenmeter"
	tokenmeterfactory "github.com/gorenx/goren/llm/tokenmeter/factory"
	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/session"
	sessionfactory "github.com/gorenx/goren/session/factory"
	"github.com/gorenx/goren/session/persistence"
	sessionpersistencefactory "github.com/gorenx/goren/session/persistence/factory"
	"github.com/gorenx/goren/session/projection"
	sessionprojectionfactory "github.com/gorenx/goren/session/projection/factory"
	"github.com/gorenx/goren/session/query"
	sessionqueryfactory "github.com/gorenx/goren/session/query/factory"
	querysqlite "github.com/gorenx/goren/session/query/sqlite"
	"github.com/gorenx/goren/session/title"
	sessiontitlefactory "github.com/gorenx/goren/session/title/factory"
	"github.com/gorenx/goren/subagent"
	subagentfactory "github.com/gorenx/goren/subagent/factory"
	subagentforkfactory "github.com/gorenx/goren/subagent/fork/factory"
	subagentplugin "github.com/gorenx/goren/subagent/plugin"
	"github.com/gorenx/goren/subagent/spawn"
	subagentspawnfactory "github.com/gorenx/goren/subagent/spawn/factory"
	subagentcontrol "github.com/gorenx/goren/subagent/tools/control"
	subagentcontrolfactory "github.com/gorenx/goren/subagent/tools/control/factory"
	subagentdelegation "github.com/gorenx/goren/subagent/tools/delegation"
	subagentdelegationfactory "github.com/gorenx/goren/subagent/tools/delegation/factory"
	"github.com/gorenx/goren/subagent/tools/report"
	subagentreportfactory "github.com/gorenx/goren/subagent/tools/report/factory"
	"github.com/gorenx/goren/systemprompt"
	systempromptfactory "github.com/gorenx/goren/systemprompt/factory"
	"github.com/gorenx/goren/toolaskuser"
	toolaskuserfactory "github.com/gorenx/goren/toolaskuser/factory"
	"github.com/gorenx/goren/tools"
	toolsfactory "github.com/gorenx/goren/tools/factory"
	"github.com/gorenx/goren/userquestions"
	userquestionsfactory "github.com/gorenx/goren/userquestions/factory"
	"github.com/gorenx/goren/web"
	webfactory "github.com/gorenx/goren/web/factory"
	"github.com/gorenx/goren/workspace"
	workspacefactory "github.com/gorenx/goren/workspace/factory"
)

// Environment contains process-derived values and technical policies supplied
// to domain-owned Factories. It contains no Plugin configuration.
type Environment struct {
	WorkingDirectory string
	LookupEnv        func(string) (string, bool)
	UserHomeDir      func() (string, error)
	EnsureDirectory  func(string) error
	Diagnostics      *Diagnostics
}

// PluginSpec is one strict Factory invocation and its Server-tree activation
// phase. Raw configuration ends at the selected domain Factory.
type PluginSpec struct {
	FactoryName string
	Config      json.RawMessage
	Phase       plugin.ActivationPhase
}

// NewCatalog registers the statically linked Factories included in the server.
// Every Factory is implemented by its owning capability package.
func NewCatalog(platform Environment) (*pluginfactory.Catalog, error) {
	if strings.TrimSpace(platform.WorkingDirectory) == "" ||
		platform.WorkingDirectory != strings.TrimSpace(platform.WorkingDirectory) {
		return nil, errors.New(
			"assembly: working directory must be non-empty and trimmed",
		)
	}
	if platform.Diagnostics == nil {
		return nil, errors.New("assembly: diagnostics are required")
	}
	lookupEnvironment := platform.LookupEnv
	if lookupEnvironment == nil {
		lookupEnvironment = os.LookupEnv
	}
	resolveHome := platform.UserHomeDir
	if resolveHome == nil {
		resolveHome = os.UserHomeDir
	}
	ensureDirectory := platform.EnsureDirectory
	if ensureDirectory == nil {
		ensureDirectory = func(path string) error {
			return os.MkdirAll(path, 0o755)
		}
	}
	hostEnvironment := processEnvironment{
		lookup:   lookupEnvironment,
		userHome: resolveHome,
	}
	credentialsBuilder, err := credentialsfactory.New(hostEnvironment)
	if err != nil {
		return nil, err
	}
	deepSeekBuilder, err := deepseek.NewFactory(hostEnvironment)
	if err != nil {
		return nil, err
	}
	sessionBuilder, err := sessionfactory.New(platform.Diagnostics)
	if err != nil {
		return nil, err
	}
	persistenceBuilder, err := sessionpersistencefactory.New(
		platform.Diagnostics,
	)
	if err != nil {
		return nil, err
	}
	titleBuilder, err := sessiontitlefactory.New(platform.Diagnostics)
	if err != nil {
		return nil, err
	}
	factories := []pluginfactory.Factory{
		agentfactory.New(agent.RegistryOptions{
			ObserverError: platform.Diagnostics.Report,
		}),
		agentdefaultmodelfactory.New(),
		agentloopfactory.New(agentloop.RuntimeOptions{
			ObserverError: platform.Diagnostics.Report,
		}),
		approvalfactory.New(),
		commandsfactory.New(commands.RuntimeOptions{
			ObserverError: platform.Diagnostics.Report,
		}),
		apiproxyfactory.New(apiproxyhost.RuntimeOptions{
			WorkingDirectory: platform.WorkingDirectory,
			EnsureDirectory:  ensureDirectory,
			ObserverError:    platform.Diagnostics.Report,
		}),
		connectionfactory.New(),
		credentialsBuilder,
		llmfactory.New(platform.Diagnostics),
		deepSeekBuilder,
		llmretryfactory.New(llmretry.RuntimeOptions{
			ObserverError: platform.Diagnostics.Report,
		}),
		tokenmeterfactory.New(),
		toolresultprunerfactory.New(),
		compactionbasicfactory.New(basic.RuntimeOptions{
			ObserverError: platform.Diagnostics.Report,
		}),
		compactioncommandfactory.New(),
		sessionBuilder,
		persistenceBuilder,
		sessionprojectionfactory.New(),
		sessionqueryfactory.New(),
		titleBuilder,
		systempromptfactory.New(),
		subagentfactory.New(subagentplugin.Diagnostics{
			ObserverError: platform.Diagnostics.Report,
		}),
		subagentspawnfactory.New(),
		subagentforkfactory.New(),
		subagentdelegationfactory.New(),
		subagentcontrolfactory.New(),
		subagentreportfactory.New(),
		toolsfactory.New(),
		toolaskuserfactory.New(),
		userquestionsfactory.New(),
		webfactory.New(),
		workspacefactory.New(),
	}
	directory := pluginfactory.NewCatalog()
	for _, builder := range factories {
		if err = directory.Register(builder); err != nil {
			return nil, err
		}
	}
	return directory, nil
}

// DefaultSpecs returns the typed default server deployment as strict Factory
// inputs. Connection is the only commit-phase Plugin because it exposes the
// process to external requests.
func DefaultSpecs(
	listenAddress string,
	version string,
	sessionDatabasePath string,
	workspaceDatabasePath string,
) ([]PluginSpec, error) {
	connectionRaw, err := json.Marshal(connectionfactory.Config{
		ListenAddress: listenAddress,
		ServeWeb:      true,
	})
	if err != nil {
		return nil, err
	}
	apiProxyRaw, err := json.Marshal(apiproxyfactory.Config{
		Version: version,
	})
	if err != nil {
		return nil, err
	}
	defaultModelRaw, err := json.Marshal(agentdefaultmodel.Config{
		Provider: deepseek.ProviderRoute,
		Model:    deepseek.DefaultModelID,
	})
	if err != nil {
		return nil, err
	}
	persistenceRaw, err := json.Marshal(sessionpersistencefactory.Config{
		Path:                     sessionDatabasePath,
		JournalMode:              "wal",
		WriteBatchMaxDelayMS:     persistence.DefaultWriteBatchMaxDelay.Milliseconds(),
		PreparedSessionCacheSize: persistence.DefaultPreparedSessionCache,
	})
	if err != nil {
		return nil, err
	}
	queryRaw, err := json.Marshal(sessionqueryfactory.Config{
		Path: filepath.Join(
			filepath.Dir(sessionDatabasePath),
			"session-query.sqlite",
		),
		JournalMode: querysqlite.JournalWAL,
	})
	if err != nil {
		return nil, err
	}
	credentialsRaw, err := json.Marshal(credentialsfactory.Config{
		Local: credentialslocal.Config{
			Path: filepath.Join(
				filepath.Dir(sessionDatabasePath),
				".credentials.json",
			),
		},
	})
	if err != nil {
		return nil, err
	}
	workspaceRaw, err := json.Marshal(workspacefactory.Config{
		Path:        workspaceDatabasePath,
		JournalMode: "wal",
	})
	if err != nil {
		return nil, err
	}
	titleRaw, err := json.Marshal(title.Config{
		FallbackMaxWords: 5,
		FallbackMaxBytes: 40,
		MaxTitleBytes:    80,
		LLM: &title.LLMConfig{
			AutomaticMode:       title.AutomaticFirstPrompt,
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
	maxSubagentDepth, err := subagentdelegationfactory.NewNumericDepthLimit(3)
	if err != nil {
		return nil, err
	}
	subagentToolRaw, err := json.Marshal(subagentdelegationfactory.Config{
		Provider:              spawn.DefaultSeedBuilderName,
		ToolName:              subagentdelegation.DefaultToolName,
		EnableRunInBackground: true,
		BackgroundMode:        subagentdelegation.BackgroundContinuable,
		MaxDepth:              maxSubagentDepth,
	})
	if err != nil {
		return nil, err
	}
	reportRaw, err := json.Marshal(subagentreportfactory.Config{
		ReportDelivery: subagent.ReportNextStep,
	})
	if err != nil {
		return nil, err
	}
	emptyConfig := json.RawMessage(`{}`)
	return []PluginSpec{
		{
			FactoryName: agent.PluginName,
			Config:      emptyConfig,
		},
		{
			FactoryName: agentdefaultmodel.PluginName,
			Config:      defaultModelRaw,
		},
		{
			FactoryName: session.PluginName,
			Config:      emptyConfig,
		},
		{
			FactoryName: persistence.PluginName,
			Config:      persistenceRaw,
		},
		{
			FactoryName: projection.PluginName,
			Config:      emptyConfig,
		},
		{
			FactoryName: subagent.PluginName,
			Config:      emptyConfig,
		},
		{
			FactoryName: spawn.PluginName,
			Config:      emptyConfig,
		},
		{
			FactoryName: subagentdelegation.PluginName,
			Config:      subagentToolRaw,
		},
		{
			FactoryName: subagentcontrol.PluginName,
			Config:      emptyConfig,
		},
		{
			FactoryName: report.PluginName,
			Config:      reportRaw,
		},
		{
			FactoryName: query.PluginName,
			Config:      queryRaw,
		},
		{
			FactoryName: credentials.PluginName,
			Config:      credentialsRaw,
		},
		{
			FactoryName: llm.PluginName,
			Config:      emptyConfig,
		},
		{
			FactoryName: deepseek.PluginName,
			Config:      emptyConfig,
		},
		{
			FactoryName: systemprompt.PluginName,
			Config:      emptyConfig,
		},
		{
			FactoryName: approval.PluginName,
			Config:      emptyConfig,
		},
		{
			FactoryName: commands.PluginName,
			Config:      emptyConfig,
		},
		{
			FactoryName: tools.PluginName,
			Config:      emptyConfig,
		},
		{
			FactoryName: userquestions.PluginName,
			Config:      emptyConfig,
		},
		{
			FactoryName: toolaskuser.PluginName,
			Config:      emptyConfig,
		},
		{
			FactoryName: workspace.PluginName,
			Config:      workspaceRaw,
		},
		{
			FactoryName: title.PluginName,
			Config:      titleRaw,
		},
		{
			FactoryName: agentloop.PluginName,
			Config:      emptyConfig,
		},
		{
			FactoryName: llmretry.PluginName,
			Config:      emptyConfig,
		},
		{
			FactoryName: tokenmeter.PluginName,
			Config:      emptyConfig,
		},
		{
			FactoryName: toolresultpruner.PluginName,
			Config:      emptyConfig,
		},
		{
			FactoryName: basic.PluginName,
			Config:      emptyConfig,
		},
		{
			FactoryName: compactioncommand.PluginName,
			Config:      emptyConfig,
		},
		{
			FactoryName: web.PluginName,
			Config:      emptyConfig,
		},
		{
			FactoryName: apiproxy.PluginName,
			Config:      apiProxyRaw,
		},
		{
			FactoryName: connectionhost.PluginName,
			Config:      connectionRaw,
			Phase:       plugin.ActivationCommit,
		},
	}, nil
}

type processEnvironment struct {
	lookup   func(string) (string, bool)
	userHome func() (string, error)
}

func (platform processEnvironment) Lookup(environmentName string) (string, bool) {
	return platform.lookup(environmentName)
}

func (platform processEnvironment) UserHomeDir() (string, error) {
	return platform.userHome()
}
