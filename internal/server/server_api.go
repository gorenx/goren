package server

import (
	"context"
	"errors"
	"os"

	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/apiproxy"
	commandsadapter "github.com/gorenx/goren/apiproxy/commands/actor"
	modeladapter "github.com/gorenx/goren/apiproxy/llm/actor"
	sessionapi "github.com/gorenx/goren/apiproxy/session"
	sessionadapter "github.com/gorenx/goren/apiproxy/session/actor"
	eventadapter "github.com/gorenx/goren/apiproxy/session/events/actor"
	workspaceadapter "github.com/gorenx/goren/apiproxy/workspace/actor"
	commandactor "github.com/gorenx/goren/commands/actor"
	credentialslocal "github.com/gorenx/goren/credentials/local"
	llmactor "github.com/gorenx/goren/llm/actor"
	"github.com/gorenx/goren/llm/deepseek"
	"github.com/gorenx/goren/session/persistence"
	cacheactor "github.com/gorenx/goren/session/projectioncache/actor"
	cachesqlite "github.com/gorenx/goren/session/projectioncache/sqlite"
	"github.com/gorenx/goren/session/query"
	queryactor "github.com/gorenx/goren/session/query/actor"
	sessionregistry "github.com/gorenx/goren/session/registry"
	"github.com/gorenx/goren/session/title"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	workspaceactor "github.com/gorenx/goren/workspace/actor"
	workspacesqlite "github.com/gorenx/goren/workspace/persistence/sqlite"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func (owner *Server) initializeAPI(ctx context.Context) error {
	system := owner.system
	timeout := owner.settings.Timeout
	secrets, err := initializeCredentials(ctx, system, credentialslocal.Config{
		Path: owner.settings.CredentialsPath,
	}, owner.platform)
	if err != nil {
		return err
	}
	_, err = initializeModels(ctx, system, DefaultModelConfig())
	if err != nil {
		return err
	}
	_, err = initializePrompts(ctx, system, owner.settings.Prompt)
	if err != nil {
		return err
	}
	models, err := goakt.GrainOf[*llmactor.Grain](ctx, system, llmactor.Name, goakt.WithLongLivedGrain())
	if err != nil {
		return err
	}
	prompts, err := goakt.GrainOf[*promptactor.Grain](ctx, system, promptactor.Name, goakt.WithLongLivedGrain())
	if err != nil {
		return err
	}
	_, err = initializeDeepSeek(ctx, system, owner.settings.Provider, owner.platform, secrets)
	if err != nil {
		return err
	}
	router, err := llmactor.SpawnPool(ctx, system, "models", 2, llmactor.InitializeWorker{
		LLM: models,
	})
	if err != nil {
		return err
	}
	bridge, err := goakt.GrainOf[*modeladapter.Grain](ctx, system, "api.models", goakt.WithLongLivedGrain(),
		goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		return err
	}
	payload, err := system.AskGrain(ctx, bridge, modeladapter.Initialize{
		Router: router,
	}, timeout)
	if err != nil {
		return err
	}
	initialized, valid := payload.(llmactor.Completion)
	if !valid {
		return errors.New("server: invalid model query initialization reply")
	}
	if initialized.Err != nil {
		return initialized.Err
	}
	modelDirectory, err := modeladapter.NewDirectory(system, models, bridge, timeout)
	if err != nil {
		return err
	}
	tools, err := initializeTools(ctx, system, owner.settings.Tools)
	if err != nil {
		return err
	}
	sessions, err := goakt.GrainOf[*sessionregistry.RegistryGrain](ctx, system, sessionregistry.RegistryName,
		goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		return err
	}
	owner.agents, err = goakt.GrainOf[*agentregistry.Grain](ctx, system, agentregistry.Name,
		goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		return err
	}

	commands, err := goakt.GrainOf[*commandactor.Registry](ctx, system, commandactor.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		return err
	}
	titleOwner, err := initializeTitles(ctx, system, title.Config{
		FallbackMaxWords: 5,
		FallbackMaxBytes: 40,
		MaxTitleBytes:    80,
	}, nil, timeout)
	if err != nil {
		return err
	}
	projections, err := initializeProjections(ctx, system, timeout)
	if err != nil {
		return err
	}
	durable, err := persistence.NewReader(owner.sessions)
	if err != nil {
		return err
	}
	owner.checkpoints, err = cachesqlite.Open(ctx, cachesqlite.Config{
		Path: owner.settings.CacheDatabase,
	})
	if err != nil {
		return err
	}
	records, err := owner.checkpoints.LoadAll(ctx)
	if err != nil {
		return err
	}
	owner.caches, err = goakt.GrainOf[*cacheactor.Registry](
		ctx, system,
		cacheactor.RegistryName,
		goakt.WithLongLivedGrain(),
	)
	if err != nil {
		return err
	}
	payload, err = system.AskGrain(ctx, owner.caches, cacheactor.ConfigureRegistry{
		Storage: owner.checkpoints,
		Records: records,
	}, timeout)
	if err != nil {
		return err
	}
	configuredCache, valid := payload.(cacheactor.Completion)
	if !valid {
		return errors.New("server: invalid cache configuration reply")
	}
	if configuredCache.Err != nil {
		return configuredCache.Err
	}
	cache, err := sessionadapter.NewCache(system, owner.caches, projections, durable, timeout)
	if err != nil {
		return err
	}
	owner.workspace, err = initializeWorkspaces(ctx, system, sessions, durable, workspacesqlite.Config{
		Path:        owner.settings.WorkspaceDatabase,
		JournalMode: workspacesqlite.JournalWAL,
	}, timeout)
	if err != nil {
		return err
	}
	workspaceClient, err := workspaceactor.NewClient(owner.workspace, timeout)
	if err != nil {
		return err
	}
	workspaces, err := workspaceadapter.NewDirectory(workspaceClient)
	if err != nil {
		return err
	}
	observations, err := queryactor.NewLogs(system, sessions, durable, timeout)
	if err != nil {
		return err
	}
	queries, err := queryactor.New(owner.index, query.Config{}, observations)
	if err != nil {
		return err
	}
	owner.queries, err = system.Spawn(ctx, "session.query", queries, goakt.WithStashing())
	if err != nil {
		return err
	}
	search, err := sessionadapter.NewSearch(owner.queries, timeout)
	if err != nil {
		return err
	}
	hooks, err := goakt.GrainOf[*middlewareactor.Grain](
		ctx, system, "middleware",
		goakt.WithLongLivedGrain(),
		goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))),
	)
	if err != nil {
		return err
	}
	agents, err := sessionadapter.NewDirectory(
		system, owner.agents,
		agentregistry.CreateAgent{
			Storage:         owner.sessions,
			SessionRegistry: sessions,
			ModelConfig:     models,
			ModelRouter:     router,
			Middleware:      hooks,
			PromptRegistry:  prompts,
			ToolsRegistry:   tools,
			Commands:        commands,
			Components:      []*goakt.GrainIdentity{titleOwner},
		},
		timeout,
	)
	if err != nil {
		return err
	}
	logs, err := sessionadapter.NewLogs(system, sessions, timeout)
	if err != nil {
		return err
	}
	views, err := sessionadapter.NewProjections(system, projections, timeout)
	if err != nil {
		return err
	}
	reader, err := sessionapi.NewReader(logs, views, agents)
	if err != nil {
		return err
	}
	titles, err := sessionadapter.NewTitles(system, owner.agents, timeout)
	if err != nil {
		return err
	}
	snapshots, err := eventadapter.NewSnapshots(system, sessions, timeout)
	if err != nil {
		return err
	}
	commandDirectory, err := commandsadapter.NewDirectory(system, owner.agents, timeout)
	if err != nil {
		return err
	}
	owner.api, err = apiproxy.NewServer(ctx, apiproxy.Dependencies{
		Sessions: sessionapi.Dependencies{
			Agents:      agents,
			Sessions:    reader,
			Persistence: durable,
			Cache:       cache,
			Models:      modelDirectory,
			Titles:      titles,
			Workspaces:  workspaces,
			Directories: directories{},
		},
		Search:      search,
		Snapshots:   snapshots,
		Workspaces:  workspaces,
		Commands:    commandDirectory,
		Credentials: secrets,
		Description: &description{
			version:  owner.settings.Version,
			cwd:      owner.settings.WorkingDirectory,
			sessions: reader,
		},
	}, apiproxy.Options{
		Sessions: sessionapi.Options{
			WorkingDirectory: owner.settings.WorkingDirectory,
		},
	})
	if err != nil {
		return err
	}
	listener, err := eventadapter.SpawnSubscriber(ctx, system, "api.sessions", owner.api.SessionEvents())
	if listener != nil {
		owner.listeners = append(owner.listeners, listener)
	}
	if err != nil {
		return err
	}
	listener, err = workspaceadapter.SpawnSubscriber(ctx, system, "api.workspaces", owner.api.WorkspaceEvents())
	if listener != nil {
		owner.listeners = append(owner.listeners, listener)
	}
	return err
}

// description reads live counts without retaining the owning process Server.
type description struct {
	version  string
	cwd      string
	sessions sessionapi.LiveSessions
}

func (source *description) DescribeHost(ctx context.Context) (apiproxy.HostDescription, error) {
	snapshots, err := source.sessions.List(ctx)
	if err != nil {
		return apiproxy.HostDescription{}, err
	}
	return apiproxy.HostDescription{
		Version:          source.version,
		CWD:              source.cwd,
		Provider:         deepseek.ProviderRoute,
		Model:            deepseek.DefaultModelID,
		AttachedSessions: len(snapshots),
	}, nil
}

type directories struct{}

func (directories) EnsureDirectory(path string) error { return os.MkdirAll(path, 0o755) }
