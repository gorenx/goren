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

type interactionProvider struct {
	seenQuestions [][]userquestions.Question
}

func (provider *interactionProvider) Ask(
	_ context.Context,
	questionRequest userquestions.Request,
) (userquestions.Answer, error) {
	provider.seenQuestions = append(
		provider.seenQuestions,
		questionRequest.Questions,
	)
	customText := "release notes"
	return userquestions.Answer{
		Answers: []userquestions.AnswerItem{
			{
				ID: "targets",
				Selected: []string{
					"tests",
					"docs",
				},
				Custom: &customText,
			},
		},
	}, nil
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
	commandContext, cancelCommand := context.WithTimeout(
		context.Background(),
		30*time.Second,
	)
	defer cancelCommand()
	sourceOutput, err := runTypeScript(
		commandContext,
		sourceRoot,
		filepath.Join(
			repositoryRoot,
			"tests",
			"contract",
			"typescript",
			"interactions.ts",
		),
		sourceRoot,
		filepath.Join(
			repositoryRoot,
			"contracts",
			"deepseek-harness",
			"manifest.json",
		),
	)
	if err != nil {
		t.Fatal(err)
	}

	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		t.Fatal(err)
	}
	approvalSettings, err := approval.ValidateConfig(approval.Config{})
	if err != nil {
		t.Fatal(err)
	}
	toolSettings, err := toolscore.ValidateConfig(toolscore.Config{})
	if err != nil {
		t.Fatal(err)
	}
	promptService := systemprompt.New(
		promptSettings,
		systemprompt.RegistryOptions{},
	)
	approvalService := approval.New(approvalSettings)
	toolService := toolscore.New(toolSettings)
	questionService := userquestions.New()
	answerer := &contractWaterfallPlugin[
		approval.DecisionRequest,
		approval.Decision,
	]{
		name: "interactions-contract-approval-answerer",
		middleware: contractWaterfallFunc[
			approval.DecisionRequest,
			approval.Decision,
		](func(
			_ context.Context,
			_ approval.DecisionRequest,
			_ plugin.WaterfallAction[
				approval.DecisionRequest,
				approval.Decision,
			],
		) (approval.Decision, error) {
			return approval.Decision{
				Outcome: approval.OutcomeAllowedOnce,
			}, nil
		}),
	}
	runtimeEngine := newContractRuntime(t)
	if _, err = runtimeEngine.Start(
		context.Background(),
		promptService,
		approvalService,
		toolService,
		questionService,
		toolaskuser.New(),
	); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if shutdownErr := runtimeEngine.Shutdown(
			context.Background(),
		); shutdownErr != nil {
			t.Error(shutdownErr)
		}
	}()

	conversation, err := session.New(
		"interaction-contract",
		session.CreateOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.Append(
		conversation,
		session.TurnStarted,
		session.TurnStart{
			Turn: 1,
		},
	); err != nil {
		t.Fatal(err)
	}
	subject := &fixture.Agent{
		Identifier:   "interaction-contract",
		Conversation: conversation,
	}
	callIdentifier := llm.CallID("call-1")
	reasonText := "needs permission"
	unavailable, err := approvalService.Request(
		context.Background(),
		approval.Request{
			Subject:  subject,
			ToolName: "shell",
			CallID:   &callIdentifier,
			Reason:   &reasonText,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtimeEngine.Mount(context.Background(), answerer); err != nil {
		t.Fatal(err)
	}
	allowed, err := approvalService.Request(
		context.Background(),
		approval.Request{
			Subject:  subject,
			ToolName: "read",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = approvalService.SetPolicy(
		context.Background(),
		subject,
		approval.PolicyNever,
	); err != nil {
		t.Fatal(err)
	}
	rejected, err := approvalService.Request(
		context.Background(),
		approval.Request{
			Subject:  subject,
			ToolName: "write",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	assembledPrompt, err := promptService.Assemble(
		context.Background(),
		systemprompt.AssembleContext{
			Session: conversation,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	observation := interactionObservation{}
	observation.Approval.Outcomes = []approval.Outcome{
		unavailable,
		allowed,
		rejected,
	}
	for _, entry := range assembledPrompt.Contexts {
		if entry.Name == "approval:policy" {
			observation.Approval.PolicyContext = entry.Text
		}
	}
	override, found, err := approvalService.OverrideOf(conversation)
	if err != nil || !found {
		t.Fatalf(
			"approval override = (%q, %t, %v)",
			override,
			found,
			err,
		)
	}
	observation.Approval.Override = override
	for _, messageValue := range subject.Injected() {
		observation.Approval.Injected = append(
			observation.Approval.Injected,
			interactionMessage{
				Content: messageValue.ContentValue(),
				Source:  messageValue.SourceValue(),
			},
		)
	}
	approvalEntries := conversation.Events()
	for index := 0; index < len(approvalEntries); index++ {
		if approvalEntries[index].Type != approval.AskedEventName {
			continue
		}
		var asked approval.Asked
		if err = json.Unmarshal(approvalEntries[index].Data, &asked); err != nil {
			t.Fatal(err)
		}
		var decided approval.Decided
		for index++; index < len(approvalEntries); index++ {
			if approvalEntries[index].Type == approval.DecidedEventName {
				if err = json.Unmarshal(
					approvalEntries[index].Data,
					&decided,
				); err != nil {
					t.Fatal(err)
				}
				break
			}
		}
		observation.Approval.Audit = append(
			observation.Approval.Audit,
			interactionAudit{
				Asked: interactionAuditAsked{
					ToolName: asked.ToolName,
					CallID:   asked.CallID,
					Reason:   asked.Reason,
				},
				Decided: interactionAuditDecided{
					Outcome:   decided.Outcome,
					IDMatches: decided.ID == asked.ID,
				},
			},
		)
	}

	detail := "# Plan"
	options := []userquestions.Option{
		{
			Label: "Approve",
		},
	}
	_, badIntentErr := questionService.Ask(
		context.Background(),
		userquestions.Request{
			Questions: []userquestions.Question{
				{
					ID:       "plan",
					Question: "Approve?",
					Detail:   &detail,
					Options:  &options,
					Intent: &userquestions.Intent{
						Kind:    userquestions.IntentPlanReview,
						Approve: "Ship it",
					},
				},
			},
		},
	)
	var questionFailure *userquestions.Error
	if !errors.As(badIntentErr, &questionFailure) {
		t.Fatalf("bad intent error = %v", badIntentErr)
	}
	observation.Questions.BadIntent = interactionError{
		Name:    questionFailure.ToolErrorName(),
		Code:    questionFailure.Code,
		Message: questionFailure.Message,
	}
	noProvider := toolService.Execute(
		context.Background(),
		toolscore.ToolExecutionInput{
			CallID:    "ask-none",
			Name:      toolaskuser.Name,
			Arguments: json.RawMessage(`{"questions":[{"id":"continue","question":"Continue?"}]}`),
		},
	)
	observation.Questions.NoProvider = observeToolResult(t, noProvider)
	providerValue := &interactionProvider{}
	if _, err = questionService.RegisterProvider(providerValue); err != nil {
		t.Fatal(err)
	}
	answered := toolService.Execute(
		context.Background(),
		toolscore.ToolExecutionInput{
			CallID: "ask-answered",
			Name:   toolaskuser.Name,
			Arguments: json.RawMessage(`{
          "questions": [{
            "id": "targets", "question": "What should I update?", "header": "Choose",
            "options": [{"label": "tests", "description": "Run tests."}, {"label": "docs"}],
            "multi_select": true
          }]
        }`),
		},
	)
	observation.Questions.SeenQuestions = providerValue.seenQuestions
	observation.Questions.Answered = observeToolResult(t, answered)
	toolSchemas := toolService.Schemas()
	if len(toolSchemas) != 1 {
		t.Fatalf("Tool schemas = %#v", toolSchemas)
	}
	observation.ToolSchema = toolSchemas[0]
	goOutput, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, goOutput, sourceOutput)
}
