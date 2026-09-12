package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/attachment/image"
	"github.com/gorenx/goren/internal/connection"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/deepseek"
	"github.com/gorenx/goren/llm/deepseek/anonymoususerid"
	"github.com/gorenx/goren/llm/modelconfig"
	"github.com/gorenx/goren/mcpclient"
	sessionsqlite "github.com/gorenx/goren/session/persistence/sqlite"
	cacheactor "github.com/gorenx/goren/session/projectioncache/actor"
	cachesqlite "github.com/gorenx/goren/session/projectioncache/sqlite"
	querysqlite "github.com/gorenx/goren/session/query/sqlite"
	usersettings "github.com/gorenx/goren/settings"
	settingsactor "github.com/gorenx/goren/settings/actor"
	"github.com/gorenx/goren/systemprompt"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	toolactor "github.com/gorenx/goren/tools/tool/actor"
	"github.com/gorenx/goren/web"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

// Server owns process resources and their dependency-ordered release. It does not
// own Agent state; Registry controls every Agent and Session lifetime.
type Server struct {
	// lifecycle serializes external Start/Close calls and Address reads. Actor
	// scheduling and domain operations never acquire this process-boundary lock.
	lifecycle   sync.Mutex
	settings    Config
	pictures    *image.Store
	platform    anonymoususerid.Environment
	started     bool
	address     string
	system      goakt.ActorSystem
	sessions    *sessionsqlite.Adapter
	index       *querysqlite.Adapter
	checkpoints *cachesqlite.Adapter
	caches      *goakt.GrainIdentity
	agents      *goakt.GrainIdentity
	workspace   *goakt.PID
	queries     *goakt.PID
	listeners   []*goakt.PID
	api         *apiproxy.Server
	carrier     *connection.HTTPHost
	cancel      context.CancelFunc
	served      chan error
	mcp         *mcpManager
	mcpIdentity *goakt.GrainIdentity
	preferences *settingsactor.Provider
	picker      *directoryPicker
}

// New validates required process inputs without starting actors or opening files.
func New(settings Config, platform anonymoususerid.Environment) (*Server, error) {
	validated, err := settings.Validate(platform)
	if err != nil {
		return nil, err
	}
	return &Server{
		settings: validated,
		platform: platform,
	}, nil
}

// Validate normalizes and checks configuration without creating runtime resources.
func (settings Config) Validate(platform anonymoususerid.Environment) (Config, error) {
	if platform == nil ||
		settings.WorkingDirectory == "" ||
		settings.SessionDatabase == "" ||
		settings.WorkspaceDatabase == "" ||
		settings.CredentialsPath == "" {
		return Config{}, errors.New("server: environment, working directory and storage paths are required")
	}
	if !filepath.IsAbs(settings.WorkingDirectory) {
		return Config{}, errors.New("server: working directory must be absolute")
	}
	if settings.Address == "" {
		settings.Address = "127.0.0.1:3080"
	}
	if settings.Timeout == 0 {
		settings.Timeout = 10 * time.Second
	}
	if settings.Timeout < 0 {
		return Config{}, errors.New("server: request timeout must be positive")
	}
	if settings.QueryDatabase == "" {
		settings.QueryDatabase = filepath.Join(filepath.Dir(settings.SessionDatabase), "query.sqlite")
	}
	if settings.CacheDatabase == "" {
		settings.CacheDatabase = filepath.Join(filepath.Dir(settings.SessionDatabase), "projections.sqlite")
	}
	if err := settings.userBaseline().Validate(); err != nil {
		return Config{}, err
	}
	captured := settings.userBaseline().Clone()
	settings.Companions = captured.Companions
	settings.Agents = captured.Agents
	settings.Tools = settings.Tools.Clone()
	settings.Providers = settings.Providers.Clone()
	settings.MCP = append([]mcpclient.Config(nil), settings.MCP...)
	serverNames := make(map[string]bool)
	for index, configured := range settings.MCP {
		validated, err := configured.Validate()
		if err != nil {
			return Config{}, err
		}
		if serverNames[validated.ServerName] {
			return Config{}, errors.New("server: duplicate MCP server name")
		}
		serverNames[validated.ServerName] = true
		settings.MCP[index] = validated
	}

	_, port, err := net.SplitHostPort(settings.Address)
	if err != nil {
		return Config{}, errors.New("server: invalid listen address")
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return Config{}, errors.New("server: invalid listen port")
	}
	connection, err := deepseek.ResolveOptions(settings.Provider, platform)
	if err != nil {
		return Config{}, errors.New("server: invalid provider configuration")
	}
	// Freeze the resolved reference and endpoint before later construction.
	settings.Provider.APIKeyEnv = &connection.APIKeyEnv
	settings.Provider.BaseURL = &connection.BaseURL
	baseline := usersettings.ProviderConfig{
		APIKeyEnv:           settings.Provider.APIKeyEnv,
		BaseURL:             settings.Provider.BaseURL,
		StreamIdleTimeoutMS: settings.Provider.StreamIdleTimeoutMS,
	}
	if err := baseline.Validate(); err != nil {
		return Config{}, err
	}
	if _, err := systemprompt.ValidateConfig(settings.Prompt); err != nil {
		return Config{}, errors.New("server: invalid prompt configuration")
	}
	if settings.Models.Default.Provider == "" && settings.Models.Default.Model == "" {
		settings.Models = DefaultModelConfig()
	}
	if settings.Models.Default.Provider != deepseek.ProviderRoute {
		return Config{}, errors.New("server: default provider is not installed")
	}
	settings.Models.Providers = []modelconfig.Provider{
		{
			ID:            deepseek.ProviderRoute,
			CredentialRef: connection.APIKeyEnv,
		},
	}
	catalog, err := modelconfig.NewCatalog(settings.Models)
	if err != nil {
		return Config{}, errors.New("server: invalid model configuration")
	}
	settings.Models = catalog.Current().Definition
	if _, err := deepseek.SerializeRequest(llm.GenerateOptions{
		CallConfig: settings.Models.Default,
	}, connection.Defaults); err != nil {
		return Config{}, errors.New("server: invalid default model parameters")
	}
	if settings.SettingsDatabase == "" {
		settings.SettingsDatabase = filepath.Join(filepath.Dir(settings.SessionDatabase), "settings.sqlite")
	}
	paths := make(map[string]bool)
	for _, path := range []string{
		settings.SessionDatabase,
		settings.WorkspaceDatabase,
		settings.QueryDatabase,
		settings.CacheDatabase,
		settings.CredentialsPath,
		settings.SettingsDatabase,
	} {
		absolute, err := filepath.Abs(path)
		if err != nil || paths[absolute] {
			return Config{}, errors.New("server: storage paths must be distinct")
		}
		paths[absolute] = true
	}
	return settings, nil
}

// Start initializes API dependencies before binding the HTTP listener. Startup
// failure rolls back acquired resources; a failed rollback remains retryable by Close.
func (owner *Server) Start(ctx context.Context) (failure error) {
	owner.lifecycle.Lock()
	defer owner.lifecycle.Unlock()
	if ctx == nil {
		return errors.New("server: startup context is required")
	}
	if owner.started {
		return errors.New("server: this instance has already started or closed")
	}
	startupFailure := ctx.Err()
	if startupFailure != nil {
		return startupFailure
	}
	owner.started = true
	defer func() {
		if failure != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), owner.settings.Timeout)
			defer cancel()
			failure = errors.Join(failure, owner.closeResources(cleanup))
		}
	}()
	var err error
	if err := owner.openSettings(ctx); err != nil {
		return err
	}
	if err := owner.openImages(); err != nil {
		return err
	}

	owner.sessions, err = sessionsqlite.Open(ctx, sessionsqlite.Config{
		Path:        owner.settings.SessionDatabase,
		JournalMode: sessionsqlite.JournalWAL,
	})
	if err != nil {
		return err
	}
	owner.index, err = querysqlite.Open(ctx, querysqlite.Config{
		Path: owner.settings.QueryDatabase,
	})
	if err != nil {
		return err
	}
	constructors := &toolactor.Providers{}
	owner.system, err = goakt.NewActorSystem("goren", goakt.WithPubSub(), goakt.WithLogger(log.DiscardLogger), goakt.WithExtensions(constructors, &runtimeactor.Provider{}))
	if err != nil {
		return err
	}
	err = owner.system.Start(ctx)
	if err != nil {
		return err
	}
	err = owner.initializeAPI(ctx, constructors)
	if err != nil {
		return err
	}
	owner.carrier, err = connection.NewHTTPHost(connection.HTTPConfig{
		Frontend: web.New(),
	}, owner.api, owner.api)
	if err != nil {
		return err
	}
	var binder net.ListenConfig
	listener, err := binder.Listen(ctx, "tcp", owner.settings.Address)
	if err != nil {
		return fmt.Errorf("server: listen: %w", err)
	}
	owner.address = listener.Addr().String()
	lifetime, cancel := context.WithCancel(context.Background())
	owner.cancel = cancel
	owner.served = make(chan error, 1)
	carrier := owner.carrier
	completion := owner.served
	go func() {
		completion <- carrier.Serve(lifetime, listener)
		close(completion)
	}()
	return nil
}

// Address returns the bound listener address after successful startup.
func (owner *Server) Address() string {
	owner.lifecycle.Lock()
	defer owner.lifecycle.Unlock()
	return owner.address
}

// Done reports unexpected transport exit as well as requested shutdown. Callers
// must still call Close to settle Agents and release storage.
func (owner *Server) Done() <-chan error {
	owner.lifecycle.Lock()
	defer owner.lifecycle.Unlock()
	return owner.served
}

// Close settles native selection, then stops network admission before Agents.
// Failure retains all dependencies needed to retry the incomplete stage.
func (owner *Server) Close(ctx context.Context) error {
	owner.lifecycle.Lock()
	defer owner.lifecycle.Unlock()
	if ctx == nil {
		return errors.New("server: shutdown context is required")
	}
	owner.started = true
	return owner.closeResources(ctx)
}

func (owner *Server) closeResources(ctx context.Context) error {
	var transportFailure error
	if owner.picker != nil {
		if err := owner.picker.Close(ctx); err != nil {
			return err
		}
		owner.picker = nil
	}
	if owner.mcp != nil {
		owner.mcp.cancel()
	}
	if owner.cancel != nil {
		owner.cancel()
		select {
		case err := <-owner.served:
			owner.cancel = nil
			if err != nil && !errors.Is(err, context.Canceled) {
				transportFailure = err
			}
		case <-ctx.Done():
			return errors.Join(transportFailure, ctx.Err())
		}
	}
	for len(owner.listeners) > 0 {
		last := len(owner.listeners) - 1
		err := owner.listeners[last].Shutdown(ctx)
		if err != nil {
			return errors.Join(transportFailure, err)
		}
		owner.listeners = owner.listeners[:last]
	}
	if owner.api != nil {
		err := owner.api.Close(ctx)
		if err != nil {
			return errors.Join(transportFailure, err)
		}
		owner.api = nil
	}
	if owner.preferences != nil {
		if err := owner.preferences.Close(ctx); err != nil {
			return errors.Join(transportFailure, err)
		}
		owner.preferences = nil
	}
	if owner.agents != nil {
		err := closeAgents(ctx, owner.system, owner.agents)
		if err != nil {
			return errors.Join(transportFailure, err)
		}
		owner.agents = nil
	}
	if owner.mcp != nil {
		if err := owner.mcp.close(ctx); err != nil {
			return err
		}
		owner.mcp = nil
	}
	if owner.workspace != nil {
		err := owner.workspace.Shutdown(ctx)
		if err != nil {
			return errors.Join(transportFailure, err)
		}
		owner.workspace = nil
	}
	if owner.queries != nil {
		err := owner.queries.Shutdown(ctx)
		if err != nil {
			return errors.Join(transportFailure, err)
		}
		owner.queries = nil
	}
	if owner.caches != nil {
		payload, err := owner.system.AskGrain(ctx, owner.caches, cacheactor.CloseRegistry{}, time.Duration(1<<63-1))
		if err != nil {
			return errors.Join(transportFailure, err)
		}
		result, valid := payload.(cacheactor.Completion)
		if !valid {
			return errors.Join(transportFailure, errors.New("server: invalid cache shutdown reply"))
		}
		if result.Err != nil {
			return result.Err
		}
		owner.caches = nil
	}
	if owner.system != nil {
		err := owner.system.Stop(ctx)
		if err != nil {
			return errors.Join(transportFailure, err)
		}
		owner.system = nil
	}
	if owner.pictures != nil {
		if err := owner.pictures.Close(ctx); err != nil {
			return errors.Join(transportFailure, err)
		}
		owner.pictures = nil
		owner.settings.Attachments = nil
	}
	if owner.index != nil {
		err := owner.index.Close(ctx)
		if err != nil {
			return errors.Join(transportFailure, err)
		}
		owner.index = nil
	}
	if owner.checkpoints != nil {
		err := owner.checkpoints.Close(ctx)
		if err != nil {
			return errors.Join(transportFailure, err)
		}
		owner.checkpoints = nil
	}
	if owner.sessions != nil {
		err := owner.sessions.Close(ctx)
		if err != nil {
			return errors.Join(transportFailure, err)
		}
		owner.sessions = nil
	}
	return transportFailure
}
