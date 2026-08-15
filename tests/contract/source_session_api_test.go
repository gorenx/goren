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
	connectionhost "github.com/gorenx/goren/internal/connection"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sessionpersistence "github.com/gorenx/goren/session/persistence"
	sqlitepersistence "github.com/gorenx/goren/session/persistence/sqlite"
	sessionprojection "github.com/gorenx/goren/session/projection"
	sessiontitle "github.com/gorenx/goren/session/title"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tests/contract/fixture"
	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/workspace"
	workspaceSqlite "github.com/gorenx/goren/workspace/persistence/sqlite"
)

type sessionAPIContractState struct {
	gateway          *apiproxy.SessionGateway
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
	state *sessionAPIContractState
}

func (*sessionAPIContractProvider) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: "session-api-contract"}
}

func (extension *sessionAPIContractProvider) Apply(requestContext context.Context, providerScope *plugin.Scope) error {
	agentRegistry, err := agent.NewRegistry(providerScope, agent.RegistryOptions{
		ObserverError: extension.state.failures.report,
	})
	if err != nil {
		return err
	}
	sessionStore, err := session.NewMemoryStore(providerScope, session.MemoryStoreOptions{
		ObserverError: extension.state.failures.report,
	})
	if err != nil {
		return err
	}
	storage, err := sqlitepersistence.Open(requestContext, sqlitepersistence.Config{
		Path: ":memory:", JournalMode: sqlitepersistence.JournalWAL,
	})
	if err != nil {
		return err
	}
	durability, err := sessionpersistence.NewSessionLogStore(
		requestContext, providerScope, sessionStore, storage,
		sessionpersistence.SessionLogStoreOptions{
			WriteBatchMaxDelay: time.Hour,
			ObserverError:      extension.state.failures.report,
		},
	)
	if err != nil {
		_ = storage.Close(requestContext)
		return err
	}
	workspaceStorage, err := workspaceSqlite.Open(requestContext, workspaceSqlite.Config{
		Path: ":memory:", JournalMode: workspaceSqlite.JournalWAL,
	})
	if err != nil {
		return err
	}
	if err := providerScope.Effect(requestContext, "contract.workspaceStorage.close()",
		func(context.Context) (plugin.Disposer, error) {
			return workspaceStorage.Close, nil
		}); err != nil {
		_ = workspaceStorage.Close(requestContext)
		return err
	}
	workspaceRegistry, err := workspace.NewRegistry(
		requestContext, providerScope, workspaceStorage,
		fixture.SessionHeaderSource{Sessions: sessionStore, Persistence: durability},
		workspace.RegistryOptions{},
	)
	if err != nil {
		return err
	}
	projectionRegistry, err := sessionprojection.NewDriveRegistry(providerScope)
	if err != nil {
		return err
	}
	titleService, err := sessiontitle.NewLogService(
		providerScope,
		sessionStore,
		projectionRegistry,
		sessiontitle.Config{FallbackMaxWords: 5, FallbackMaxBytes: 40, MaxTitleBytes: 80},
		sessiontitle.Options{ReportError: extension.state.failures.report},
	)
	if err != nil {
		return err
	}
	modelRuntime, err := llm.NewRuntime(providerScope, nil)
	if err != nil {
		return err
	}
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		return err
	}
	promptRuntime, err := systemprompt.New(requestContext, providerScope, promptSettings)
	if err != nil {
		return err
	}
	toolSettings, err := tools.ValidateConfig(tools.Config{})
	if err != nil {
		return err
	}
	toolRuntime, err := tools.New(requestContext, providerScope, promptRuntime, nil, nil, toolSettings)
	if err != nil {
		return err
	}
	loopSettings, err := agentloop.ValidateConfig(agentloop.Config{})
	if err != nil {
		return err
	}
	if _, err := agentloop.New(requestContext, providerScope, agentloop.Dependencies{
		Agents: agentRegistry, Sessions: sessionStore, LLM: modelRuntime,
		Tools: toolRuntime, SystemPrompt: promptRuntime,
	}, loopSettings, agentloop.RuntimeOptions{ObserverError: extension.state.failures.report}); err != nil {
		return err
	}
	if _, err := agent.OnError(providerScope, func(_ context.Context, notice agent.ErrorNotice) error {
		extension.state.failures.report(notice.Err)
		return nil
	}); err != nil {
		return err
	}
	if _, err := modelRuntime.RegisterAdapter(
		requestContext, providerScope, []string{"mock"}, extension.state.backend,
	); err != nil {
		return err
	}
	defaultSelection, err := agentdefaultmodel.NewStatic(agent.ModelSelection{
		Provider: "mock", Model: "mock-model",
	})
	if err != nil {
		return err
	}
	gateway, err := apiproxy.NewSessionGateway(requestContext, providerScope, apiproxy.SessionGatewayDependencies{
		Agents: agentRegistry, Sessions: sessionStore, Persistence: durability,
		LLM: modelRuntime, Defaults: defaultSelection,
		Projections: projectionRegistry, Titles: titleService,
		Workspaces: workspaceRegistry,
		Directories: apiproxy.DirectoryProvisionerFunc(func(string) error {
			return nil
		}),
	}, apiproxy.SessionGatewayOptions{WorkingDirectory: "/contract-workspace"})
	if err != nil {
		return err
	}
	workspaceGateway, err := apiproxy.NewWorkspaceGateway(
		requestContext, providerScope, workspaceRegistry, gateway,
	)
	if err != nil {
		return err
	}
	extension.state.gateway = gateway
	extension.state.workspaceGateway = workspaceGateway
	return nil
}

type sessionAPIContractAdapter struct {
	callCount atomic.Int32
}

func (*sessionAPIContractAdapter) DescribeProvider(providerRoute string) (llm.ProviderInfo, error) {
	if providerRoute != "mock" {
		return llm.ProviderInfo{}, fmt.Errorf("unexpected provider route %q", providerRoute)
	}
	return llm.ProviderInfo{ID: "mock", Name: "Mock"}, nil
}

func (*sessionAPIContractAdapter) ListModels(
	_ context.Context,
	providerRoute string,
) ([]llm.ModelInfo, error) {
	if providerRoute != "mock" {
		return nil, fmt.Errorf("unexpected provider route %q", providerRoute)
	}
	return []llm.ModelInfo{{
		Provider: "mock", ID: "mock-model", Name: "Mock Model",
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
	return llm.ResolvedModelInfo{ModelInfo: llm.ModelInfo{
		Provider: "mock", ID: "mock-model", Name: "Mock Model",
		InputModalities: []llm.ModelModality{llm.ModalityText},
	}}, nil
}

func (backend *sessionAPIContractAdapter) Stream(
	_ context.Context,
	_ llm.GenerateOptions,
) (llm.ChunkStream, error) {
	switch backend.callCount.Add(1) {
	case 1:
		return llm.NewSliceStream([]llm.StreamChunk{
			llm.BlockEndChunk{Index: 0, Block: llm.NewTextBlock("first response")},
			llm.FinishChunk{Reason: llm.StopFinish{}},
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
	contractState := &sessionAPIContractState{backend: &sessionAPIContractAdapter{}}
	engine := plugin.NewRuntime()
	if _, err := engine.Load(context.Background(), &sessionAPIContractProvider{state: contractState}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if contractState.gateway == nil {
		t.Fatal("session API contract provider did not publish its gateway")
	}

	methods := apiproxy.NewCatalog()
	if err := apiproxy.RegisterSessionAPI(methods, contractState.gateway); err != nil {
		t.Fatal(err)
	}
	if err := apiproxy.RegisterWorkspaceAPI(methods, contractState.workspaceGateway); err != nil {
		t.Fatal(err)
	}
	streams, err := apiproxy.NewEventStreams(contractState.gateway.Mux, contractState.gateway.Host)
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
	if !observation.FirstPrompt || !observation.FallbackTitle || !slices.Contains(observation.FirstHistoryTypes, session.UserMessageEventName) ||
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
	contractState := &sessionAPIContractState{backend: &sessionAPIContractAdapter{}}
	engine := plugin.NewRuntime()
	if _, err := engine.Load(context.Background(), &sessionAPIContractProvider{state: contractState}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if contractState.gateway == nil || contractState.workspaceGateway == nil {
		t.Fatal("Workspace contract provider did not publish its gateways")
	}

	methods := apiproxy.NewCatalog()
	if err := apiproxy.RegisterSessionAPI(methods, contractState.gateway); err != nil {
		t.Fatal(err)
	}
	if err := apiproxy.RegisterWorkspaceAPI(methods, contractState.workspaceGateway); err != nil {
		t.Fatal(err)
	}
	streams, err := apiproxy.NewEventStreams(contractState.gateway.Mux, contractState.gateway.Host)
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
		InitialEmpty: true, Created: true, Repeated: true, Renamed: true, RenameFrame: true,
		Attached: true, NameConflict: true, Reordered: true, OrderFrame: true,
		Archived: true, ArchiveFrame: true, ArchiveIdempotent: true,
		Deleted: true, RegistrationOnlyDelete: true, NotFoundErrors: true,
		MoveInvalid: true, UnknownSession: true,
		WorkspaceAndCWDRejected: true, WorkspaceCWD: true,
		FinalList: true, InvalidPath: true,
	}
	if observation != want {
		t.Fatalf("Workspace observation = %#v, want %#v", observation, want)
	}
}
