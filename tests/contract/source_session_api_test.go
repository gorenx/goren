//go:build contract

package contract_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentdefaultmodel"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/apiproxy"
	sessionapi "github.com/gorenx/goren/apiproxy/session"
	connectionhost "github.com/gorenx/goren/internal/connection"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	sesssqlite "github.com/gorenx/goren/session/persistence/sqlite"
	sessionprojection "github.com/gorenx/goren/session/projection"
	sessionquery "github.com/gorenx/goren/session/query"
	querysqlite "github.com/gorenx/goren/session/query/sqlite"
	sessiontitle "github.com/gorenx/goren/session/title"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/workspace"
	workspaceSqlite "github.com/gorenx/goren/workspace/persistence/sqlite"
)

type sessionAPIContractState struct {
	gateway          *sessionapi.Gateway
	downlinks        *apiproxy.LiveFrameSource
	searchGateway    *sessionapi.SearchGateway
	workspaceGateway *apiproxy.WorkspaceGateway
	backend          *sessionAPIContractAdapter
	failures         contractFailureLog
}

type contractFailureLog struct {
	mutex   sync.Mutex
	entries []error
}

func (observations *contractFailureLog) report(problem error) {
	observations.mutex.Lock()
	observations.entries = append(observations.entries, problem)
	observations.mutex.Unlock()
}

func (observations *contractFailureLog) collectedErrors() []error {
	observations.mutex.Lock()
	defer observations.mutex.Unlock()
	return append([]error(nil), observations.entries...)
}

type sessionAPIContractProvider struct {
	plugin.Base
	state *sessionAPIContractState
}

func (*sessionAPIContractProvider) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "session-api-contract",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[agent.Registry](),
			plugin.ServiceOf[agentdefaultmodel.DefaultModel](),
			plugin.ServiceOf[llm.LlmRuntime](),
			plugin.ServiceOf[session.LiveStore](),
			plugin.ServiceOf[sesspersist.Persistence](),
			plugin.ServiceOf[sessionprojection.Registry](),
			plugin.ServiceOf[sessionquery.QueryService](),
			plugin.ServiceOf[sessiontitle.TitleService](),
			plugin.ServiceOf[workspace.Registry](),
		},
		Events: []plugin.EventSubscription{
			plugin.EventOf[session.SessionEventAppended](),
			plugin.EventOf[session.SessionCreated](),
			plugin.EventOf[session.SessionDisposed](),
			plugin.EventOf[agent.StatusChanged](),
			plugin.EventOf[agent.AgentError](),
			plugin.EventOf[sessionprojection.ProjectionChanged](),
			plugin.EventOf[workspace.ChangedNotice](),
			plugin.EventOf[workspace.RemovedNotice](),
			plugin.EventOf[workspace.OrderChangedNotice](),
			plugin.EventOf[workspace.ArchivedSessionsChangedNotice](),
		},
	}
}

func (extension *sessionAPIContractProvider) Apply(
	requestContext context.Context,
) error {
	agentRegistry, err := plugin.Require[agent.Registry](extension)
	if err != nil {
		return err
	}
	defaultSelection, err := plugin.Require[agentdefaultmodel.DefaultModel](extension)
	if err != nil {
		return err
	}
	modelRuntime, err := plugin.Require[llm.LlmRuntime](extension)
	if err != nil {
		return err
	}
	sessionStore, err := plugin.Require[session.LiveStore](extension)
	if err != nil {
		return err
	}
	durability, err := plugin.Require[sesspersist.Persistence](extension)
	if err != nil {
		return err
	}
	projectionRegistry, err := plugin.Require[sessionprojection.Registry](extension)
	if err != nil {
		return err
	}
	queries, err := plugin.Require[sessionquery.QueryService](extension)
	if err != nil {
		return err
	}
	titleService, err := plugin.Require[sessiontitle.TitleService](extension)
	if err != nil {
		return err
	}
	workspaceRegistry, err := plugin.Require[workspace.Registry](extension)
	if err != nil {
		return err
	}
	gateway, err := sessionapi.NewGateway(
		requestContext,
		sessionapi.Dependencies{
			Agents:      agentRegistry,
			Sessions:    sessionStore,
			Persistence: durability,
			LLM:         modelRuntime,
			Defaults:    defaultSelection,
			Projections: projectionRegistry,
			Titles:      titleService,
			Workspaces:  workspaceRegistry,
			Directories: sessionapi.DirectoryProvisionerFunc(
				func(string) error {
					return nil
				},
			),
		},
		sessionapi.Options{
			WorkingDirectory: "/contract-workspace",
		},
	)
	if err != nil {
		return err
	}
	downlinks, err := apiproxy.NewLiveFrameSource(
		apiproxy.LiveFrameDependencies{
			Sessions:    sessionStore,
			Projections: projectionRegistry,
		},
		apiproxy.LiveFrameOptions{},
	)
	if err != nil {
		return err
	}
	searchGateway, err := sessionapi.NewSearchGateway(queries, gateway)
	if err != nil {
		return err
	}
	workspaceGateway, err := apiproxy.NewWorkspaceGateway(
		workspaceRegistry,
		downlinks,
	)
	if err != nil {
		return err
	}
	extension.state.gateway = gateway
	extension.state.downlinks = downlinks
	extension.state.searchGateway = searchGateway
	extension.state.workspaceGateway = workspaceGateway
	return requestContext.Err()
}

func (extension *sessionAPIContractProvider) Dispose(context.Context) error {
	if extension.state.downlinks != nil {
		extension.state.downlinks.Close()
	}
	return nil
}

func (extension *sessionAPIContractProvider) ObserveEvent(
	requestContext context.Context,
	fact plugin.Event,
) error {
	if notice, matches := fact.(agent.AgentError); matches {
		extension.state.failures.report(notice.Err)
	}
	var frameErr error
	if extension.state.downlinks != nil {
		frameErr = extension.state.downlinks.ObserveEvent(requestContext, fact)
	}
	var workspaceErr error
	if extension.state.workspaceGateway != nil {
		workspaceErr = extension.state.workspaceGateway.ObserveEvent(
			requestContext,
			fact,
		)
	}
	return errors.Join(frameErr, workspaceErr)
}

type contractSessionBackendOpener struct{}

func (contractSessionBackendOpener) OpenBackend(
	requestContext context.Context,
) (sesspersist.Backend, error) {
	return sesssqlite.Open(requestContext, sesssqlite.Config{
		Path:        ":memory:",
		JournalMode: sesssqlite.JournalWAL,
	})
}

type contractQueryIndexOpener struct{}

func (contractQueryIndexOpener) OpenIndex(
	requestContext context.Context,
) (sessionquery.Index, error) {
	return querysqlite.Open(requestContext, querysqlite.Config{
		Path: ":memory:",
	})
}

type contractWorkspaceBackendOpener struct{}

func (contractWorkspaceBackendOpener) OpenBackend(
	requestContext context.Context,
) (workspace.Backend, error) {
	return workspaceSqlite.Open(requestContext, workspaceSqlite.Config{
		Path:        ":memory:",
		JournalMode: workspaceSqlite.JournalWAL,
	})
}

func (observations *contractFailureLog) ReportPostCommitFailure(
	failure session.PostCommitFailure,
) {
	observations.report(failure.Error)
}

func (observations *contractFailureLog) ReportBackgroundWriteFailure(
	failure sesspersist.BackgroundWriteFailure,
) {
	observations.report(failure.Error)
}

func (observations *contractFailureLog) ReportAsyncFailure(
	failure sessiontitle.AsyncFailure,
) {
	observations.report(failure.Error)
}

func (observations *contractFailureLog) ReportObserverFailure(problem error) {
	observations.report(problem)
}

func startSessionAPIContractState(
	testingContext testing.TB,
	contractState *sessionAPIContractState,
) (*plugin.Runtime, error) {
	agentRegistry := agent.NewRegistry(agent.RegistryOptions{
		ObserverError: contractState.failures.report,
	})
	sessionStore, err := session.NewMemoryStore(session.MemoryStoreOptions{
		PostCommitFailures: &contractState.failures,
	})
	if err != nil {
		return nil, err
	}
	durability, err := sesspersist.NewSessionLogStore(
		contractSessionBackendOpener{},
		sesspersist.SessionLogStoreOptions{
			WriteBatchMaxDelay:      time.Hour,
			BackgroundWriteFailures: &contractState.failures,
		},
	)
	if err != nil {
		return nil, err
	}
	queries, err := sessionquery.New(
		contractQueryIndexOpener{},
		sessionquery.Config{},
	)
	if err != nil {
		return nil, err
	}
	workspaceRegistry, err := workspace.NewRegistry(
		contractWorkspaceBackendOpener{},
		workspace.RegistryOptions{},
	)
	if err != nil {
		return nil, err
	}
	projectionRegistry := sessionprojection.NewDriveRegistry()
	titleService, err := sessiontitle.NewLogService(
		sessiontitle.Config{
			FallbackMaxWords: 5,
			FallbackMaxBytes: 40,
			MaxTitleBytes:    80,
		},
		&contractState.failures,
	)
	if err != nil {
		return nil, err
	}
	modelRuntime := llm.NewRuntime(&contractState.failures)
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		return nil, err
	}
	promptRuntime := systemprompt.New(
		promptSettings,
		systemprompt.RegistryOptions{},
	)
	toolSettings, err := tools.ValidateConfig(tools.Config{})
	if err != nil {
		return nil, err
	}
	toolRuntime := tools.New(toolSettings)
	loopRuntime, err := agentloop.New(
		agentloop.Settings{
			MaxParallelToolCalls: agentloop.DefaultMaxParallelToolCalls,
		},
		agentloop.RuntimeOptions{
			ObserverError: contractState.failures.report,
		},
	)
	if err != nil {
		return nil, err
	}
	defaultSelection, err := agentdefaultmodel.NewStatic(agent.ModelSelection{
		Provider: "mock",
		Model:    "mock-model",
	})
	if err != nil {
		return nil, err
	}
	runtimeEngine := newContractRuntime(testingContext)
	if _, err = runtimeEngine.Start(
		context.Background(),
		agentRegistry,
		sessionStore,
		durability,
		queries,
		workspaceRegistry,
		projectionRegistry,
		titleService,
		modelRuntime,
		promptRuntime,
		toolRuntime,
		loopRuntime,
		defaultSelection,
		&sessionAPIContractProvider{
			state: contractState,
		},
	); err != nil {
		return nil, err
	}
	if _, err = modelRuntime.RegisterAdapter(
		context.Background(),
		[]string{
			"mock",
		},
		contractState.backend,
	); err != nil {
		_ = runtimeEngine.Shutdown(context.Background())
		return nil, err
	}
	return runtimeEngine, nil
}

type sessionAPIContractAdapter struct {
	callCount atomic.Int32
}

func (*sessionAPIContractAdapter) DescribeProvider(providerRoute string) (llm.ProviderInfo, error) {
	if providerRoute != "mock" {
		return llm.ProviderInfo{}, fmt.Errorf("unexpected provider route %q", providerRoute)
	}
	return llm.ProviderInfo{
		ID:   "mock",
		Name: "Mock",
	}, nil
}

func (*sessionAPIContractAdapter) ListModels(
	_ context.Context,
	providerRoute string,
) ([]llm.ModelInfo, error) {
	if providerRoute != "mock" {
		return nil, fmt.Errorf("unexpected provider route %q", providerRoute)
	}
	return []llm.ModelInfo{{
		Provider:        "mock",
		ID:              "mock-model",
		Name:            "Mock Model",
		InputModalities: []llm.ModelModality{llm.ModalityText},
	}}, nil
}

func (*sessionAPIContractAdapter) ResolveModel(
	_ context.Context,
	providerRoute string,
	modelID string,
) (llm.ResolvedModelInfo, error) {
	if providerRoute != "mock" || modelID != "mock-model" {
		return llm.ResolvedModelInfo{}, fmt.Errorf("unexpected model route %q/%q", providerRoute, modelID)
	}
	return llm.ResolvedModelInfo{
		ModelInfo: llm.ModelInfo{
			Provider:        "mock",
			ID:              "mock-model",
			Name:            "Mock Model",
			InputModalities: []llm.ModelModality{llm.ModalityText},
		},
	}, nil
}

func (backend *sessionAPIContractAdapter) Stream(
	_ context.Context,
	_ llm.GenerateOptions,
) (llm.ChunkStream, error) {
	switch backend.callCount.Add(1) {
	case 1:
		return llm.NewSliceStream([]llm.StreamChunk{
			llm.BlockEndChunk{
				Index: 0,
				Block: llm.NewTextBlock("first response"),
			},
			llm.FinishChunk{
				Reason: llm.StopFinish{},
			},
		})
	case 2:
		return &sessionAPIContractBlockingStream{}, nil
	default:
		return nil, errors.New("session API contract adapter received an unexpected request")
	}
}

type sessionAPIContractBlockingStream struct{}

func (*sessionAPIContractBlockingStream) Next(requestContext context.Context) (llm.StreamChunk, bool, error) {
	<-requestContext.Done()
	return nil, false, requestContext.Err()
}

func (*sessionAPIContractBlockingStream) Close(context.Context) error {
	return nil
}

type sessionAPIClientObservation struct {
	Created struct {
		SessionID string `json:"sessionId"`
	} `json:"created"`
	PresetConflict    bool     `json:"presetConflict"`
	Listed            bool     `json:"listed"`
	EmptyHistory      bool     `json:"emptyHistory"`
	Models            bool     `json:"models"`
	Selected          bool     `json:"selected"`
	FirstPrompt       bool     `json:"firstPrompt"`
	Searched          bool     `json:"searched"`
	InitialProjection bool     `json:"initialProjection"`
	FallbackTitle     bool     `json:"fallbackTitle"`
	Renamed           bool     `json:"renamed"`
	RuntimeProjection bool     `json:"runtimeProjection"`
	RenameFrame       bool     `json:"renameFrame"`
	RenamedList       bool     `json:"renamedList"`
	RenamedHistory    bool     `json:"renamedHistory"`
	TitleInvalid      bool     `json:"titleInvalid"`
	FirstHistoryTypes []string `json:"firstHistoryTypes"`
	SecondPrompt      bool     `json:"secondPrompt"`
	Cancelled         bool     `json:"cancelled"`
	Aborted           bool     `json:"aborted"`
	FinalHistoryTypes []string `json:"finalHistoryTypes"`
}

func TestPinnedSourceSessionWebApiClientTalksToGoGateway(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	contractState := &sessionAPIContractState{
		backend: &sessionAPIContractAdapter{},
	}
	runtimeEngine, err := startSessionAPIContractState(t, contractState)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtimeEngine.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if contractState.gateway == nil {
		t.Fatal("session API contract provider did not publish its gateway")
	}

	methods := apiproxy.NewCatalog()
	if err := apiproxy.RegisterSessionAPI(methods, contractState.gateway, contractState.searchGateway); err != nil {
		t.Fatal(err)
	}
	if err := apiproxy.RegisterWorkspaceAPI(methods, contractState.workspaceGateway); err != nil {
		t.Fatal(err)
	}
	streams, err := apiproxy.NewEventStreams(contractState.downlinks.Mux, contractState.downlinks.Host)
	if err != nil {
		t.Fatal(err)
	}
	httpHost, err := connectionhost.NewHTTPHost(connectionhost.HTTPConfig{}, methods, streams)
	if err != nil {
		t.Fatal(err)
	}
	testServer := httptest.NewServer(httpHost)
	defer testServer.Close()
	defer func() {
		closeContext, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelClose()
		if err := httpHost.Close(closeContext); err != nil {
			t.Errorf("close Go host: %v", err)
		}
	}()

	commandContext, cancelCommand := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancelCommand()
	output, err := runTypeScript(commandContext, sourceRoot,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "session-client.ts"),
		sourceRoot, testServer.URL,
	)
	if err != nil {
		t.Fatalf(
			"%v; LLM request count = %d; contained failures = %v",
			err, contractState.backend.callCount.Load(), contractState.failures.collectedErrors(),
		)
	}
	var observation sessionAPIClientObservation
	if err := json.Unmarshal(output, &observation); err != nil {
		t.Fatalf("decode source Session client observation: %v; output = %s", err, output)
	}
	if observation.Created.SessionID != "session-client-contract" || !observation.PresetConflict || !observation.Listed ||
		!observation.EmptyHistory || !observation.InitialProjection || !observation.Models || !observation.Selected {
		t.Fatalf("Session setup observation = %#v", observation)
	}
	if !observation.FirstPrompt || !observation.Searched || !observation.FallbackTitle || !slices.Contains(observation.FirstHistoryTypes, session.UserMessageEventName) ||
		!slices.Contains(observation.FirstHistoryTypes, session.TurnEndEventName) {
		t.Fatalf("first turn observation = %#v", observation)
	}
	if !observation.Renamed || !observation.RuntimeProjection || !observation.RenameFrame ||
		!observation.RenamedList || !observation.RenamedHistory || !observation.TitleInvalid {
		t.Fatalf("rename observation = %#v", observation)
	}
	if !observation.SecondPrompt || !observation.Cancelled || !observation.Aborted ||
		!slices.Contains(observation.FinalHistoryTypes, session.TurnEndEventName) {
		t.Fatalf("cancelled turn observation = %#v", observation)
	}
	if contractState.backend.callCount.Load() != 2 {
		t.Fatalf("LLM request count = %d, want 2", contractState.backend.callCount.Load())
	}
	if failures := contractState.failures.collectedErrors(); len(failures) != 0 {
		t.Fatalf("contained service failures = %v", failures)
	}
}

type workspaceClientObservation struct {
	InitialEmpty            bool `json:"initialEmpty"`
	Created                 bool `json:"created"`
	Repeated                bool `json:"repeated"`
	Renamed                 bool `json:"renamed"`
	RenameFrame             bool `json:"renameFrame"`
	Attached                bool `json:"attached"`
	NameConflict            bool `json:"nameConflict"`
	Reordered               bool `json:"reordered"`
	OrderFrame              bool `json:"orderFrame"`
	Archived                bool `json:"archived"`
	ArchiveFrame            bool `json:"archiveFrame"`
	ArchiveIdempotent       bool `json:"archiveIdempotent"`
	Deleted                 bool `json:"deleted"`
	RegistrationOnlyDelete  bool `json:"registrationOnlyDelete"`
	NotFoundErrors          bool `json:"notFoundErrors"`
	MoveInvalid             bool `json:"moveInvalid"`
	UnknownSession          bool `json:"unknownSession"`
	WorkspaceAndCWDRejected bool `json:"workspaceAndCwdRejected"`
	WorkspaceCWD            bool `json:"workspaceCwd"`
	FinalList               bool `json:"finalList"`
	InvalidPath             bool `json:"invalidPath"`
}

func TestPinnedSourceWorkspaceWebApiClientTalksToGoGateway(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	contractState := &sessionAPIContractState{
		backend: &sessionAPIContractAdapter{},
	}
	runtimeEngine, err := startSessionAPIContractState(t, contractState)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtimeEngine.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if contractState.gateway == nil || contractState.workspaceGateway == nil {
		t.Fatal("Workspace contract provider did not publish its gateways")
	}

	methods := apiproxy.NewCatalog()
	if err := apiproxy.RegisterSessionAPI(methods, contractState.gateway, contractState.searchGateway); err != nil {
		t.Fatal(err)
	}
	if err := apiproxy.RegisterWorkspaceAPI(methods, contractState.workspaceGateway); err != nil {
		t.Fatal(err)
	}
	streams, err := apiproxy.NewEventStreams(contractState.downlinks.Mux, contractState.downlinks.Host)
	if err != nil {
		t.Fatal(err)
	}
	httpHost, err := connectionhost.NewHTTPHost(connectionhost.HTTPConfig{}, methods, streams)
	if err != nil {
		t.Fatal(err)
	}
	testServer := httptest.NewServer(httpHost)
	defer testServer.Close()
	defer func() {
		closeContext, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelClose()
		if err := httpHost.Close(closeContext); err != nil {
			t.Errorf("close Go host: %v", err)
		}
	}()

	dataDirectory := t.TempDir()
	firstPath := filepath.Join(dataDirectory, "first")
	secondPath := filepath.Join(dataDirectory, "second")
	for _, directory := range []string{firstPath, secondPath} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	commandContext, cancelCommand := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancelCommand()
	output, err := runTypeScript(commandContext, sourceRoot,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "workspace-client.ts"),
		sourceRoot, testServer.URL, firstPath, secondPath,
	)
	if err != nil {
		t.Fatal(err)
	}
	var observation workspaceClientObservation
	if err := json.Unmarshal(output, &observation); err != nil {
		t.Fatalf("decode source Workspace client observation: %v; output = %s", err, output)
	}
	want := workspaceClientObservation{
		InitialEmpty:            true,
		Created:                 true,
		Repeated:                true,
		Renamed:                 true,
		RenameFrame:             true,
		Attached:                true,
		NameConflict:            true,
		Reordered:               true,
		OrderFrame:              true,
		Archived:                true,
		ArchiveFrame:            true,
		ArchiveIdempotent:       true,
		Deleted:                 true,
		RegistrationOnlyDelete:  true,
		NotFoundErrors:          true,
		MoveInvalid:             true,
		UnknownSession:          true,
		WorkspaceAndCWDRejected: true,
		WorkspaceCWD:            true,
		FinalList:               true,
		InvalidPath:             true,
	}
	if observation != want {
		t.Fatalf("Workspace observation = %#v, want %#v", observation, want)
	}
}
