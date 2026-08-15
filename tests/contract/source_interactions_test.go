//go:build contract

package contract_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tests/contract/fixture"
	"github.com/gorenx/goren/toolaskuser"
	toolscore "github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/userquestions"
)

type interactionContractPlugin struct {
	fixture *interactionFixture
}

type interactionFixture struct {
	engine          *plugin.Runtime
	pluginScope     *plugin.Scope
	promptService   systemprompt.SystemPrompt
	approvalService approval.Approval
	toolService     toolscore.ToolRuntime
	questionService userquestions.UserQuestions
}

func (*interactionContractPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "interactions-contract",
		Provides: []plugin.ServiceRef{
			systemprompt.Service.Ref(), approval.Service.Ref(), toolscore.Service.Ref(), userquestions.Service.Ref(),
		},
	}
}

func (instance *interactionContractPlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		return err
	}
	promptService, err := systemprompt.New(requestContext, pluginScope, promptSettings)
	if err != nil {
		return err
	}
	approvalSettings, err := approval.ValidateConfig(approval.Config{})
	if err != nil {
		return err
	}
	approvalService, err := approval.New(
		requestContext, pluginScope, promptService, approvalSettings, approval.RuntimeOptions{},
	)
	if err != nil {
		return err
	}
	toolSettings, err := toolscore.ValidateConfig(toolscore.Config{})
	if err != nil {
		return err
	}
	toolService, err := toolscore.New(requestContext, pluginScope, promptService, nil, nil, toolSettings)
	if err != nil {
		return err
	}
	questionService := userquestions.New(nil)
	definition, err := toolaskuser.New(questionService)
	if err != nil {
		return err
	}
	if _, err := toolService.Register(requestContext, pluginScope, definition); err != nil {
		return err
	}
	if _, err := plugin.Provide(pluginScope, systemprompt.Service, promptService); err != nil {
		return err
	}
	if _, err := plugin.Provide(pluginScope, approval.Service, approvalService); err != nil {
		return err
	}
	if _, err := plugin.Provide(pluginScope, toolscore.Service, toolService); err != nil {
		return err
	}
	if _, err := plugin.Provide(pluginScope, userquestions.Service, questionService); err != nil {
		return err
	}
	instance.fixture.pluginScope = pluginScope
	instance.fixture.promptService = promptService
	instance.fixture.approvalService = approvalService
	instance.fixture.toolService = toolService
	instance.fixture.questionService = questionService
	return nil
}

type interactionProvider struct {
	seenQuestions [][]userquestions.Question
}

func (provider *interactionProvider) Ask(_ context.Context, questionRequest userquestions.Request) (userquestions.Answer, error) {
	provider.seenQuestions = append(provider.seenQuestions, questionRequest.Questions)
	customText := "release notes"
	return userquestions.Answer{Answers: []userquestions.AnswerItem{{
		ID: "targets", Selected: []string{"tests", "docs"}, Custom: &customText,
	}}}, nil
}

type interactionAuditAsked struct {
	ToolName string      `json:"toolName"`
	CallID   *llm.CallID `json:"callId,omitempty"`
	Reason   *string     `json:"reason,omitempty"`
}

type interactionAuditDecided struct {
	Outcome   approval.Outcome `json:"outcome"`
	IDMatches bool             `json:"idMatches"`
}

type interactionAudit struct {
	Asked   interactionAuditAsked   `json:"asked"`
	Decided interactionAuditDecided `json:"decided"`
}

type interactionMessage struct {
	Content []llm.ContentBlock `json:"content"`
	Source  llm.MessageSource  `json:"source"`
}

type interactionError struct {
	Name    string `json:"name"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type interactionObservation struct {
	Approval struct {
		Outcomes      []approval.Outcome   `json:"outcomes"`
		Audit         []interactionAudit   `json:"audit"`
		Override      approval.Policy      `json:"override"`
		PolicyContext string               `json:"policyContext"`
		Injected      []interactionMessage `json:"injected"`
	} `json:"approval"`
	Questions struct {
		BadIntent     interactionError           `json:"badIntent"`
		NoProvider    toolResultObservation      `json:"noProvider"`
		SeenQuestions [][]userquestions.Question `json:"seenQuestions"`
		Answered      toolResultObservation      `json:"answered"`
	} `json:"questions"`
	ToolSchema llm.ToolSchema `json:"toolSchema"`
}

func TestPinnedSourceInteractionsMatchGo(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	commandContext, cancelCommand := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelCommand()
	sourceOutput, err := runTypeScript(commandContext, sourceRoot,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "interactions.ts"),
		sourceRoot, filepath.Join(repositoryRoot, "contracts", "deepseek-harness", "manifest.json"),
	)
	if err != nil {
		t.Fatal(err)
	}

	state := &interactionFixture{engine: plugin.NewRuntime()}
	if _, err := state.engine.Load(context.Background(), &interactionContractPlugin{fixture: state}); err != nil {
		t.Fatal(err)
	}
	conversation, err := session.New("interaction-contract", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Append(conversation, session.TurnStarted, session.TurnStart{Turn: 1}); err != nil {
		t.Fatal(err)
	}
	agentScope, _, err := state.pluginScope.Child("interaction-agent")
	if err != nil {
		t.Fatal(err)
	}
	subject := &fixture.Agent{
		Identifier: "interaction-contract", Conversation: conversation, AgentScope: agentScope,
	}
	callIdentifier := llm.CallID("call-1")
	reasonText := "needs permission"
	unavailable, err := state.approvalService.Request(context.Background(), approval.Request{
		Subject: subject, ToolName: "shell", CallID: &callIdentifier, Reason: &reasonText,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := approval.OnRequest(state.pluginScope,
		func(context.Context, approval.Request, approval.RequestNext) (approval.Outcome, error) {
			return approval.OutcomeAllowedOnce, nil
		}); err != nil {
		t.Fatal(err)
	}
	allowed, err := state.approvalService.Request(context.Background(), approval.Request{
		Subject: subject, ToolName: "read",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.approvalService.SetPolicy(context.Background(), subject, approval.PolicyNever); err != nil {
		t.Fatal(err)
	}
	rejected, err := state.approvalService.Request(context.Background(), approval.Request{
		Subject: subject, ToolName: "write",
	})
	if err != nil {
		t.Fatal(err)
	}

	assembled, err := state.promptService.Assemble(context.Background(), systemprompt.AssembleContext{
		Scope: agentScope.Target(), Session: conversation,
	})
	if err != nil {
		t.Fatal(err)
	}
	observation := interactionObservation{}
	observation.Approval.Outcomes = []approval.Outcome{unavailable, allowed, rejected}
	for _, entry := range assembled.Contexts {
		if entry.Name == "approval:policy" {
			observation.Approval.PolicyContext = entry.Text
		}
	}
	override, found, err := state.approvalService.OverrideOf(conversation)
	if err != nil || !found {
		t.Fatalf("approval override = (%q, %t, %v)", override, found, err)
	}
	observation.Approval.Override = override
	for _, messageValue := range subject.Injected() {
		observation.Approval.Injected = append(observation.Approval.Injected, interactionMessage{
			Content: messageValue.ContentValue(), Source: messageValue.SourceValue(),
		})
	}
	approvalEntries := conversation.Events()
	for index := 0; index < len(approvalEntries); index++ {
		if approvalEntries[index].Type != approval.AskedEventName {
			continue
		}
		var asked approval.Asked
		if err := json.Unmarshal(approvalEntries[index].Data, &asked); err != nil {
			t.Fatal(err)
		}
		var decided approval.Decided
		for index++; index < len(approvalEntries); index++ {
			if approvalEntries[index].Type == approval.DecidedEventName {
				if err := json.Unmarshal(approvalEntries[index].Data, &decided); err != nil {
					t.Fatal(err)
				}
				break
			}
		}
		observation.Approval.Audit = append(observation.Approval.Audit, interactionAudit{
			Asked:   interactionAuditAsked{ToolName: asked.ToolName, CallID: asked.CallID, Reason: asked.Reason},
			Decided: interactionAuditDecided{Outcome: decided.Outcome, IDMatches: decided.ID == asked.ID},
		})
	}

	detail := "# Plan"
	options := []userquestions.Option{{Label: "Approve"}}
	_, badIntentErr := state.questionService.Ask(context.Background(), userquestions.Request{
		Questions: []userquestions.Question{{
			ID: "plan", Question: "Approve?", Detail: &detail, Options: &options,
			Intent: &userquestions.Intent{Kind: userquestions.IntentPlanReview, Approve: "Ship it"},
		}},
	})
	var questionFailure *userquestions.Error
	if !errors.As(badIntentErr, &questionFailure) {
		t.Fatalf("bad intent error = %v", badIntentErr)
	}
	observation.Questions.BadIntent = interactionError{
		Name: questionFailure.ToolErrorName(), Code: questionFailure.Code, Message: questionFailure.Message,
	}
	noProvider := state.toolService.Execute(context.Background(), toolscore.ToolExecutionInput{
		CallID: "ask-none", Name: toolaskuser.Name,
		Arguments: json.RawMessage(`{"questions":[{"id":"continue","question":"Continue?"}]}`),
	})
	observation.Questions.NoProvider = observeToolResult(t, noProvider)
	providerValue := &interactionProvider{}
	if _, err := state.questionService.RegisterProvider(context.Background(), state.pluginScope, providerValue); err != nil {
		t.Fatal(err)
	}
	answered := state.toolService.Execute(context.Background(), toolscore.ToolExecutionInput{
		CallID: "ask-answered", Name: toolaskuser.Name,
		Arguments: json.RawMessage(`{
          "questions": [{
            "id": "targets", "question": "What should I update?", "header": "Choose",
            "options": [{"label": "tests", "description": "Run tests."}, {"label": "docs"}],
            "multi_select": true
          }]
        }`),
	})
	observation.Questions.SeenQuestions = providerValue.seenQuestions
	observation.Questions.Answered = observeToolResult(t, answered)
	toolSchemas := state.toolService.Schemas(plugin.ScopeKey{})
	if len(toolSchemas) != 1 {
		t.Fatalf("tool schemas = %#v", toolSchemas)
	}
	observation.ToolSchema = toolSchemas[0]
	goOutput, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.engine.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, goOutput, sourceOutput)
}
