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
	"github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/approval"
	connectionhost "github.com/gorenx/goren/internal/connection"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tests/contract/fixture"
	"github.com/gorenx/goren/userquestions"
)

type interactionAPIContractState struct {
	agents          *agent.RegistryPlugin
	agentsHandle    plugin.Handle
	sessions        *session.MemoryStore
	approvalService *approval.Service
	questionService *userquestions.QuestionService
	methods         *apiproxy.Catalog
	downlinks       *apiproxy.LiveFrameSource
}

type interactionAPIContractProvider struct {
	plugin.Base
	state     *interactionAPIContractState
	owner     *apiproxy.InteractionGateway
	questions *userquestions.ProviderHandle
}

func (provider *interactionAPIContractProvider) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "interaction-api-contract",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[session.LiveStore](),
			plugin.ServiceOf[sessionprojection.Registry](),
			plugin.ServiceOf[userquestions.UserQuestions](),
		},
		Waterfalls: []plugin.WaterfallMiddlewareBinding{
			plugin.WaterfallOf[
				approval.DecisionRequest,
				approval.Decision,
			](provider),
		},
	}
}

func (extension *interactionAPIContractProvider) Apply(
	requestContext context.Context,
) error {
	sessionStore, err := plugin.Require[session.LiveStore](extension)
	if err != nil {
		return err
	}
	projectionRegistry, err := plugin.Require[sessionprojection.Registry](extension)
	if err != nil {
		return err
	}
	questionService, err := plugin.Require[userquestions.UserQuestions](extension)
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
	methods := apiproxy.NewCatalog()
	owner, err := apiproxy.NewInteractionGateway(
		apiproxy.InteractionGatewayDependencies{
			Methods: methods,
			Frames:  downlinks.InteractionBroker(),
		},
		apiproxy.InteractionGatewayOptions{},
	)
	if err != nil {
		return err
	}
	providerHandle, err := questionService.RegisterProvider(owner)
	if err != nil {
		downlinks.Close()
		return err
	}
	extension.owner = owner
	extension.questions = providerHandle
	extension.state.methods = methods
	extension.state.downlinks = downlinks
	return requestContext.Err()
}

func (extension *interactionAPIContractProvider) Dispose(
	closeContext context.Context,
) error {
	if extension.questions != nil {
		extension.questions.Unregister()
	}
	var closeErr error
	if extension.owner != nil {
		closeErr = extension.owner.Close(closeContext)
	}
	if extension.state.downlinks != nil {
		extension.state.downlinks.Close()
	}
	extension.owner = nil
	extension.questions = nil
	return closeErr
}

func (extension *interactionAPIContractProvider) Intercept(
	requestContext context.Context,
	input approval.DecisionRequest,
	downstream plugin.WaterfallAction[
		approval.DecisionRequest,
		approval.Decision,
	],
) (approval.Decision, error) {
	return extension.owner.ResolveApproval(requestContext, input, downstream)
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
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		t.Fatal(err)
	}
	approvalSettings, err := approval.ValidateConfig(approval.Config{})
	if err != nil {
		t.Fatal(err)
	}
	agentRegistry := agent.NewRegistry(agent.RegistryOptions{})
	sessionStore, err := session.NewMemoryStore(session.MemoryStoreOptions{
		PostCommitFailures: contractPostCommitReporter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	questionService := userquestions.New()
	contractState.agents = agentRegistry
	contractState.sessions = sessionStore
	contractState.approvalService = approval.New(approvalSettings)
	contractState.questionService = questionService
	runtimeEngine := newContractRuntime(t)
	handles, err := runtimeEngine.Start(
		context.Background(),
		systemprompt.New(promptSettings, systemprompt.RegistryOptions{}),
		contractState.approvalService,
		agentRegistry,
		sessionStore,
		sessionprojection.NewDriveRegistry(),
		questionService,
		&interactionAPIContractProvider{
			state: contractState,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	contractState.agentsHandle = handles[2]
	t.Cleanup(func() {
		if err := runtimeEngine.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	conversationID := session.SessionID("interaction-api-session")
	workingDirectory := "/contract-workspace"
	sessionHandle, err := contractState.sessions.Create(
		context.Background(),
		&conversationID,
		session.CreateOptions{
			Metadata: session.Metadata{
				CWD: &workingDirectory,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	conversation := sessionHandle.Session()
	subject := &fixture.Agent{
		Identifier:   conversationID,
		Conversation: conversation,
		Registry:     agentRegistry,
	}
	if _, err = runtimeEngine.MountScopedChild(
		context.Background(),
		contractState.agentsHandle,
		subject,
	); err != nil {
		t.Fatal(err)
	}
	if err = contractState.agents.Enter(subject, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Append(conversation, session.TurnStarted, session.TurnStart{
		Turn: 1,
	}); err != nil {
		t.Fatal(err)
	}

	approvalChannel := make(chan approval.Outcome, 1)
	approvalErrorChannel := make(chan error, 1)
	callIdentifier := llm.CallID("interaction-call")
	go func() {
		decision, requestErr := contractState.approvalService.Request(context.Background(), approval.Request{
			Subject:  subject,
			ToolName: "bash",
			CallID:   &callIdentifier,
			Reason:   stringPointer("needs permission"),
		})
		approvalChannel <- decision
		approvalErrorChannel <- requestErr
	}()
	firstMuxContext, cancelFirstMux := context.WithCancel(context.Background())
	firstMuxFrames := make(chan apiproxy.StreamRequest[apiproxy.MuxFrame], 16)
	firstMuxDone := make(chan error, 1)
	go func() {
		firstMuxDone <- contractState.downlinks.Mux(firstMuxContext, func(envelope apiproxy.StreamRequest[apiproxy.MuxFrame]) error {
			firstMuxFrames <- envelope
			return nil
		})
	}()
	waitContractMuxType(t, firstMuxFrames, "approval/requested")

	questionChannel := make(chan userquestions.Answer, 1)
	questionErrorChannel := make(chan error, 1)
	multiSelect := true
	options := []userquestions.Option{{
		Label: "Code",
	}, {
		Label: "Docs",
	}}
	go func() {
		answerValue, askErr := contractState.questionService.Ask(context.Background(), userquestions.Request{
			Subject: subject,
			Questions: []userquestions.Question{{
				ID:          "targets",
				Question:    "Choose targets",
				Options:     &options,
				MultiSelect: &multiSelect,
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

	streams, err := apiproxy.NewEventStreams(contractState.downlinks.Mux, contractState.downlinks.Host)
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
	wantAnswer := userquestions.Answer{
		Answers: []userquestions.AnswerItem{{
			ID:       "targets",
			Selected: []string{"Code", "Docs"},
			Custom:   &customText,
		}},
	}
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
