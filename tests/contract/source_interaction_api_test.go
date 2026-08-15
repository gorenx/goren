//go:build contract

package contract_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentdefaultmodel"
	"github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/approval"
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
	"github.com/gorenx/goren/userquestions"
)

type interactionAPIContractState struct {
	pluginScope     *plugin.Scope
	agents          agent.Registry
	sessions        session.Store
	approvalService approval.Approval
	questionService userquestions.UserQuestions
	methods         *apiproxy.Catalog
	sessionGateway  *apiproxy.SessionGateway
}

type interactionAPIContractProvider struct {
	state *interactionAPIContractState
}

func (*interactionAPIContractProvider) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: "interaction-api-contract"}
}

func (extension *interactionAPIContractProvider) Apply(
	requestContext context.Context,
	providerScope *plugin.Scope,
) error {
	agentRegistry, err := agent.NewRegistry(providerScope, agent.RegistryOptions{})
	if err != nil {
		return err
	}
	sessionStore, err := session.NewMemoryStore(providerScope, session.MemoryStoreOptions{})
	if err != nil {
		return err
	}
	storage, err := sqlitepersistence.Open(requestContext, sqlitepersistence.Config{
		Path: ":memory:", JournalMode: sqlitepersistence.JournalWAL,
	})
	if err != nil {
		return err
	}
	durability, err := sessionpersistence.NewCoordinator(
		requestContext, providerScope, sessionStore, storage,
		sessionpersistence.CoordinatorOptions{WriteBatchMaxDelay: time.Hour},
	)
	if err != nil {
		_ = storage.Close(requestContext)
		return err
	}
	projectionRegistry, err := sessionprojection.NewDriveRegistry(providerScope)
	if err != nil {
		return err
	}
	titleService, err := sessiontitle.NewLogService(
		providerScope, sessionStore, projectionRegistry,
		sessiontitle.Config{FallbackMaxWords: 5, FallbackMaxBytes: 40, MaxTitleBytes: 80},
		sessiontitle.Options{},
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
	promptService, err := systemprompt.New(requestContext, providerScope, promptSettings)
	if err != nil {
		return err
	}
	approvalSettings, err := approval.ValidateConfig(approval.Config{})
	if err != nil {
		return err
	}
	approvalService, err := approval.New(
		requestContext, providerScope, promptService, approvalSettings, approval.RuntimeOptions{},
	)
	if err != nil {
		return err
	}
	questionService := userquestions.New(userquestions.AgentRegistryResolverFunc(func() (agent.Registry, bool) {
		return agentRegistry, true
	}))
	defaultSelection, err := agentdefaultmodel.NewStatic(agent.ModelSelection{
		Provider: "mock", Model: "mock-model",
	})
	if err != nil {
		return err
	}
	sessionGateway, err := apiproxy.NewSessionGateway(
		requestContext,
		providerScope,
		apiproxy.SessionGatewayDependencies{
			Agents: agentRegistry, Sessions: sessionStore, Persistence: durability,
			LLM: modelRuntime, Defaults: defaultSelection,
			Projections: projectionRegistry, Titles: titleService,
			Directories: apiproxy.DirectoryProvisionerFunc(func(string) error {
				return nil
			}),
		},
		apiproxy.SessionGatewayOptions{WorkingDirectory: "/contract-workspace"},
	)
	if err != nil {
		return err
	}
	methods := apiproxy.NewCatalog()
	if _, err := apiproxy.NewInteractionGateway(
		requestContext,
		providerScope,
		apiproxy.InteractionGatewayDependencies{
			Methods: methods, Frames: sessionGateway.InteractionBroker(), UserQuestions: questionService,
		},
		apiproxy.InteractionGatewayOptions{},
	); err != nil {
		return err
	}
	extension.state.pluginScope = providerScope
	extension.state.agents = agentRegistry
	extension.state.sessions = sessionStore
	extension.state.approvalService = approvalService
	extension.state.questionService = questionService
	extension.state.methods = methods
	extension.state.sessionGateway = sessionGateway
	return nil
}

type interactionClientObservation struct {
	Approval struct {
		BadReceipt       connectionReceipt `json:"badReceipt"`
		AcceptedReceipt  connectionReceipt `json:"acceptedReceipt"`
		DuplicateReceipt connectionReceipt `json:"duplicateReceipt"`
		ResolvedOutcome  string            `json:"resolvedOutcome"`
		CorrelationKept  bool              `json:"correlationKept"`
	} `json:"approval"`
	Question struct {
		BadReceipt       connectionReceipt `json:"badReceipt"`
		AcceptedReceipt  connectionReceipt `json:"acceptedReceipt"`
		DuplicateReceipt connectionReceipt `json:"duplicateReceipt"`
		ResolvedOutcome  string            `json:"resolvedOutcome"`
		CorrelationKept  bool              `json:"correlationKept"`
	} `json:"question"`
}

type connectionReceipt struct {
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
}

func TestPinnedSourceWebApiClientAnswersGoInteractions(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	contractState := &interactionAPIContractState{}
	engine := plugin.NewRuntime()
	if _, err := engine.Load(context.Background(), &interactionAPIContractProvider{state: contractState}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	agentScope, _, err := contractState.pluginScope.Child("interaction-api-agent")
	if err != nil {
		t.Fatal(err)
	}
	conversationID := session.SessionID("interaction-api-session")
	workingDirectory := "/contract-workspace"
	conversation, err := contractState.sessions.Create(
		context.Background(),
		agentScope,
		&conversationID,
		session.CreateOptions{Metadata: session.Metadata{CWD: &workingDirectory}},
	)
	if err != nil {
		t.Fatal(err)
	}
	subject := &fixture.Agent{
		Identifier: conversationID, Conversation: conversation, AgentScope: agentScope,
	}
	if _, err := contractState.agents.Register(context.Background(), agentScope, subject, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Append(conversation, session.TurnStarted, session.TurnStart{Turn: 1}); err != nil {
		t.Fatal(err)
	}

	approvalChannel := make(chan approval.Outcome, 1)
	approvalErrorChannel := make(chan error, 1)
	callIdentifier := llm.CallID("interaction-call")
	go func() {
		decision, requestErr := contractState.approvalService.Request(context.Background(), approval.Request{
			Subject: subject, ToolName: "bash", CallID: &callIdentifier, Reason: stringPointer("needs permission"),
		})
		approvalChannel <- decision
		approvalErrorChannel <- requestErr
	}()
	firstMuxContext, cancelFirstMux := context.WithCancel(context.Background())
	firstMuxFrames := make(chan apiproxy.StreamRequest[apiproxy.MuxFrame], 16)
	firstMuxDone := make(chan error, 1)
	go func() {
		firstMuxDone <- contractState.sessionGateway.Mux(firstMuxContext, func(envelope apiproxy.StreamRequest[apiproxy.MuxFrame]) error {
			firstMuxFrames <- envelope
			return nil
		})
	}()
	waitContractMuxType(t, firstMuxFrames, "approval/requested")

	questionChannel := make(chan userquestions.Answer, 1)
	questionErrorChannel := make(chan error, 1)
	multiSelect := true
	options := []userquestions.Option{{Label: "Code"}, {Label: "Docs"}}
	go func() {
		answerValue, askErr := contractState.questionService.Ask(context.Background(), userquestions.Request{
			Subject: subject,
			Questions: []userquestions.Question{{
				ID: "targets", Question: "Choose targets", Options: &options, MultiSelect: &multiSelect,
			}},
		})
		questionChannel <- answerValue
		questionErrorChannel <- askErr
	}()
	waitContractMuxType(t, firstMuxFrames, "question/requested")
	cancelFirstMux()
	if err := <-firstMuxDone; err != nil {
		t.Fatal(err)
	}

	streams, err := apiproxy.NewEventStreams(contractState.sessionGateway.Mux, contractState.sessionGateway.Host)
	if err != nil {
		t.Fatal(err)
	}
	httpHost, err := connectionhost.NewHTTPHost(connectionhost.HTTPConfig{}, contractState.methods, streams)
	if err != nil {
		t.Fatal(err)
	}
	testServer := httptest.NewServer(httpHost)
	defer testServer.Close()
	defer func() {
		closeContext, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelClose()
		if err := httpHost.Close(closeContext); err != nil {
			t.Errorf("close Go interaction host: %v", err)
		}
	}()

	commandContext, cancelCommand := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancelCommand()
	sourceOutput, err := runTypeScript(commandContext, sourceRoot,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "interaction-client.ts"),
		sourceRoot, testServer.URL,
	)
	if err != nil {
		t.Fatal(err)
	}
	var observation interactionClientObservation
	if err := json.Unmarshal(sourceOutput, &observation); err != nil {
		t.Fatalf("decode interaction client observation: %v; output = %s", err, sourceOutput)
	}
	assertInteractionReceipts(t, observation.Approval.BadReceipt, observation.Approval.AcceptedReceipt, observation.Approval.DuplicateReceipt)
	assertInteractionReceipts(t, observation.Question.BadReceipt, observation.Question.AcceptedReceipt, observation.Question.DuplicateReceipt)
	if observation.Approval.ResolvedOutcome != "allowed-once" || !observation.Approval.CorrelationKept {
		t.Fatalf("approval client observation = %#v", observation.Approval)
	}
	if observation.Question.ResolvedOutcome != "answered" || !observation.Question.CorrelationKept {
		t.Fatalf("question client observation = %#v", observation.Question)
	}

	if requestErr := <-approvalErrorChannel; requestErr != nil {
		t.Fatal(requestErr)
	}
	if decision := <-approvalChannel; decision != approval.OutcomeAllowedOnce {
		t.Fatalf("Go approval decision = %q", decision)
	}
	if askErr := <-questionErrorChannel; askErr != nil {
		t.Fatal(askErr)
	}
	answerValue := <-questionChannel
	customText := "release notes"
	wantAnswer := userquestions.Answer{Answers: []userquestions.AnswerItem{{
		ID: "targets", Selected: []string{"Code", "Docs"}, Custom: &customText,
	}}}
	if !reflect.DeepEqual(answerValue, wantAnswer) {
		t.Fatalf("Go question answer = %#v, want %#v", answerValue, wantAnswer)
	}
}

func waitContractMuxType(
	t *testing.T,
	frames <-chan apiproxy.StreamRequest[apiproxy.MuxFrame],
	wantType string,
) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case envelope := <-frames:
			encoded, err := apiproxy.EncodeMuxFrame(envelope.Payload)
			if err != nil {
				t.Fatal(err)
			}
			var discriminant struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(encoded, &discriminant); err != nil {
				t.Fatal(err)
			}
			if discriminant.Type == wantType {
				return
			}
		case <-deadline:
			t.Fatalf("mux frame %q timed out", wantType)
		}
	}
}

func assertInteractionReceipts(
	t *testing.T,
	badReceipt connectionReceipt,
	acceptedReceipt connectionReceipt,
	duplicateReceipt connectionReceipt,
) {
	t.Helper()
	if badReceipt.Accepted || badReceipt.Reason != "bad-response" {
		t.Fatalf("bad interaction receipt = %#v", badReceipt)
	}
	if !acceptedReceipt.Accepted || acceptedReceipt.Reason != "" {
		t.Fatalf("accepted interaction receipt = %#v", acceptedReceipt)
	}
	if duplicateReceipt.Accepted || duplicateReceipt.Reason != "not-pending" {
		t.Fatalf("duplicate interaction receipt = %#v", duplicateReceipt)
	}
}

func stringPointer(value string) *string {
	return &value
}
