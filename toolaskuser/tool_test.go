package toolaskuser_test

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/toolaskuser"
	toolscore "github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/userquestions"
)

type schemaNode struct {
	Type                 string                `json:"type"`
	AdditionalProperties *bool                 `json:"additionalProperties,omitempty"`
	Properties           map[string]schemaNode `json:"properties,omitempty"`
	Required             []string              `json:"required,omitempty"`
	Items                *schemaNode           `json:"items,omitempty"`
}

type askUserFixturePlugin struct {
	fixture *askUserFixture
}

type askUserFixture struct {
	engine          *plugin.Runtime
	pluginScope     *plugin.Scope
	toolService     toolscore.ToolRuntime
	questionService userquestions.UserQuestions
	releaseTool     plugin.Disposer
}

func (*askUserFixturePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "ask-user-tool-fixture",
		Provides: []plugin.ServiceRef{
			systemprompt.Service.Ref(), toolscore.Service.Ref(), userquestions.Service.Ref(),
		},
	}
}

func (instance *askUserFixturePlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		return err
	}
	promptService, err := systemprompt.New(requestContext, pluginScope, promptSettings)
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
	releaseTool, err := toolService.Register(requestContext, pluginScope, definition)
	if err != nil {
		return err
	}
	if _, err := plugin.Provide(pluginScope, systemprompt.Service, promptService); err != nil {
		return err
	}
	if _, err := plugin.Provide(pluginScope, toolscore.Service, toolService); err != nil {
		return err
	}
	if _, err := plugin.Provide(pluginScope, userquestions.Service, questionService); err != nil {
		return err
	}
	instance.fixture.pluginScope = pluginScope
	instance.fixture.toolService = toolService
	instance.fixture.questionService = questionService
	instance.fixture.releaseTool = releaseTool
	return nil
}

type providerRecorder struct {
	requestContext context.Context
	requestValue   userquestions.Request
	answerValue    userquestions.Answer
}

func (recorder *providerRecorder) Ask(requestContext context.Context, questionRequest userquestions.Request) (userquestions.Answer, error) {
	recorder.requestContext = requestContext
	recorder.requestValue = questionRequest
	return recorder.answerValue, nil
}

func newAskUserFixture(t *testing.T) *askUserFixture {
	t.Helper()
	state := &askUserFixture{engine: plugin.NewRuntime()}
	if _, err := state.engine.Load(context.Background(), &askUserFixturePlugin{fixture: state}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.engine.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return state
}

func resultText(t *testing.T, outcome toolscore.ToolExecutionResult) string {
	t.Helper()
	content := outcome.ContentBlocks()
	if len(content) != 1 {
		t.Fatalf("result content = %#v", content)
	}
	textBlock, ok := content[0].(llm.TextBlock)
	if !ok {
		t.Fatalf("result content type = %T", content[0])
	}
	return textBlock.Text
}

func TestDefinitionMatchesPinnedSchema(t *testing.T) {
	t.Parallel()
	state := newAskUserFixture(t)
	definition, found := state.toolService.Get(toolaskuser.Name, plugin.ScopeKey{})
	if !found {
		t.Fatal("ask_user_question is not registered")
	}
	if definition.Description != toolaskuser.Description {
		t.Fatalf("description = %q", definition.Description)
	}
	var parameters schemaNode
	if err := json.Unmarshal(definition.Parameters, &parameters); err != nil {
		t.Fatal(err)
	}
	questionsNode := parameters.Properties["questions"]
	itemNode := questionsNode.Items
	if parameters.Type != "object" || parameters.AdditionalProperties != nil ||
		!reflect.DeepEqual(parameters.Required, []string{"questions"}) || itemNode == nil ||
		itemNode.AdditionalProperties == nil || !*itemNode.AdditionalProperties ||
		!reflect.DeepEqual(itemNode.Required, []string{"id", "question"}) {
		t.Fatalf("parameter schema = %#v", parameters)
	}
	wantFields := []string{"header", "id", "multi_select", "options", "question"}
	gotFields := make([]string, 0, len(itemNode.Properties))
	for fieldName := range itemNode.Properties {
		gotFields = append(gotFields, fieldName)
	}
	// The literal is stable; sorting avoids relying on map iteration.
	sort.Strings(gotFields)
	if !reflect.DeepEqual(gotFields, wantFields) {
		t.Fatalf("question fields = %#v", gotFields)
	}
	optionNode := itemNode.Properties["options"].Items
	if optionNode == nil || optionNode.AdditionalProperties == nil || !*optionNode.AdditionalProperties ||
		!reflect.DeepEqual(optionNode.Required, []string{"label"}) {
		t.Fatalf("option schema = %#v", optionNode)
	}
	var output schemaNode
	if err := json.Unmarshal(definition.Output.Schema, &output); err != nil {
		t.Fatal(err)
	}
	answerNode := output.Properties["answers"].Items
	if output.AdditionalProperties == nil || *output.AdditionalProperties || answerNode == nil ||
		answerNode.AdditionalProperties == nil || *answerNode.AdditionalProperties ||
		!reflect.DeepEqual(answerNode.Required, []string{"id", "selected"}) {
		t.Fatalf("output schema = %#v", output)
	}
}

func TestExecutionDelegatesAndRendersStructuredAnswer(t *testing.T) {
	t.Parallel()
	state := newAskUserFixture(t)
	customText := "release notes"
	providerValue := &providerRecorder{answerValue: userquestions.Answer{Answers: []userquestions.AnswerItem{{
		ID: "targets", Selected: []string{"tests", "docs"}, Custom: &customText,
	}}}}
	if _, err := state.questionService.RegisterProvider(context.Background(), state.pluginScope, providerValue); err != nil {
		t.Fatal(err)
	}
	contextKey := struct{}{}
	requestContext := context.WithValue(context.Background(), contextKey, "exact")
	outcome := state.toolService.Execute(requestContext, toolscore.ToolExecutionInput{
		CallID: "ask-1", Name: toolaskuser.Name,
		Arguments: json.RawMessage(`{
          "questions": [{
            "id": "targets",
            "question": "What should I update?",
            "header": "Choose",
            "options": [{"label": "tests", "description": "Run tests.", "ignored": true}, {"label": "docs"}],
            "multi_select": true,
            "ignored": "open item"
          }],
          "ignored": "open root"
        }`),
	})
	if outcome.Failed() {
		t.Fatalf("execution failed: %s", resultText(t, outcome))
	}
	if providerValue.requestContext.Value(contextKey) != "exact" {
		t.Fatal("tool did not preserve execution context values")
	}
	questions := providerValue.requestValue.Questions
	if len(questions) != 1 || questions[0].Header == nil || *questions[0].Header != "Choose" ||
		questions[0].MultiSelect == nil || !*questions[0].MultiSelect || questions[0].Options == nil ||
		len(*questions[0].Options) != 2 || (*questions[0].Options)[0].Label != "tests" {
		t.Fatalf("provider request = %#v", providerValue.requestValue)
	}
	wantText := `{"answers":[{"id":"targets","selected":["tests","docs"],"custom":"release notes"}]}`
	if got := resultText(t, outcome); got != wantText {
		t.Fatalf("rendered answer = %q", got)
	}
}

func TestExecutionPreservesStructuredQuestionErrorAndDisposal(t *testing.T) {
	t.Parallel()
	state := newAskUserFixture(t)
	outcome := state.toolService.Execute(context.Background(), toolscore.ToolExecutionInput{
		CallID: "ask-no-provider", Name: toolaskuser.Name,
		Arguments: json.RawMessage(`{"questions":[{"id":"continue","question":"Continue?"}]}`),
	})
	failure, ok := outcome.(*toolscore.ToolExecutionFailure)
	if !ok || failure.Error.Info == nil || failure.Error.Info.Name != "UserQuestionError" ||
		failure.Error.Info.Code != userquestions.CodeNoProvider {
		t.Fatalf("failure = %#v", outcome)
	}
	if err := state.releaseTool(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, found := state.toolService.Get(toolaskuser.Name, plugin.ScopeKey{}); found {
		t.Fatal("disposed ask_user_question remains registered")
	}
}
