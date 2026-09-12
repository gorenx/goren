package toolaskuser

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	toolscore "github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/tools/execution"
	"github.com/gorenx/goren/userquestions"
)

type schemaNode struct {
	Type                 string                `json:"type"`
	AdditionalProperties *bool                 `json:"additionalProperties,omitempty"`
	Properties           map[string]schemaNode `json:"properties,omitempty"`
	Required             []string              `json:"required,omitempty"`
	Items                *schemaNode           `json:"items,omitempty"`
}

type providerRecorder struct {
	requestContext context.Context
	requestValue   userquestions.Request
	answerValue    userquestions.Answer
}

func (recorder *providerRecorder) Ask(
	requestContext context.Context,
	questionRequest userquestions.Request,
) (userquestions.Answer, error) {
	recorder.requestContext = requestContext
	recorder.requestValue = questionRequest
	return recorder.answerValue, nil
}

func resultText(t *testing.T, outcome toolscore.ToolExecutionResult) string {
	t.Helper()
	content := outcome.ContentBlocks()
	if len(content) != 1 {
		t.Fatalf("result content = %#v", content)
	}
	textBlock, matches := content[0].(agentmessage.TextBlock)
	if !matches {
		t.Fatalf("result content type = %T", content[0])
	}
	return textBlock.Text
}

func TestDefinitionMatchesPinnedSchema(t *testing.T) {
	t.Parallel()
	definition, err := NewDefinition(userquestions.New(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if definition.Description != Description {
		t.Fatalf("description = %q", definition.Description)
	}
	var parameters schemaNode
	if err := json.Unmarshal(definition.Parameters, &parameters); err != nil {
		t.Fatal(err)
	}
	questionsNode := parameters.Properties["questions"]
	itemNode := questionsNode.Items
	if parameters.Type != "object" || parameters.AdditionalProperties != nil ||
		!reflect.DeepEqual(parameters.Required, []string{"questions"}) ||
		itemNode == nil || itemNode.AdditionalProperties == nil ||
		!*itemNode.AdditionalProperties ||
		!reflect.DeepEqual(itemNode.Required, []string{"id", "question"}) {
		t.Fatalf("parameter schema = %#v", parameters)
	}
	wantFields := []string{"header", "id", "multi_select", "options", "question"}
	gotFields := make([]string, 0, len(itemNode.Properties))
	for fieldName := range itemNode.Properties {
		gotFields = append(gotFields, fieldName)
	}
	sort.Strings(gotFields)
	if !reflect.DeepEqual(gotFields, wantFields) {
		t.Fatalf("question fields = %#v", gotFields)
	}
	optionNode := itemNode.Properties["options"].Items
	if optionNode == nil || optionNode.AdditionalProperties == nil ||
		!*optionNode.AdditionalProperties ||
		!reflect.DeepEqual(optionNode.Required, []string{"label"}) {
		t.Fatalf("option schema = %#v", optionNode)
	}
	var output schemaNode
	if err := json.Unmarshal(definition.Output.Schema, &output); err != nil {
		t.Fatal(err)
	}
	answerNode := output.Properties["answers"].Items
	if output.AdditionalProperties == nil || *output.AdditionalProperties ||
		answerNode == nil || answerNode.AdditionalProperties == nil ||
		*answerNode.AdditionalProperties ||
		!reflect.DeepEqual(answerNode.Required, []string{"id", "selected"}) {
		t.Fatalf("output schema = %#v", output)
	}
}

func TestExecutionDelegatesAndRendersStructuredAnswer(t *testing.T) {
	t.Parallel()
	customText := "release notes"
	providerValue := &providerRecorder{
		answerValue: userquestions.Answer{
			Answers: []userquestions.AnswerItem{
				{
					ID:       "targets",
					Selected: []string{"tests", "docs"},
					Custom:   &customText,
				},
			},
		},
	}
	definition, err := NewDefinition(userquestions.New(providerValue, nil))
	if err != nil {
		t.Fatal(err)
	}
	type requestContextKey struct{}
	contextKey := requestContextKey{}
	requestContext := context.WithValue(context.Background(), contextKey, "exact")
	outcome := executeQuestion(t, definition,
		requestContext,
		toolscore.ToolExecutionInput{
			CallID: "ask-1",
			Name:   Name,
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
		},
	)
	if outcome.Failed() {
		t.Fatalf("execution failed: %s", resultText(t, outcome))
	}
	if providerValue.requestContext.Value(contextKey) != "exact" {
		t.Fatal("tool did not preserve execution context values")
	}
	questions := providerValue.requestValue.Questions
	if len(questions) != 1 || questions[0].Header == nil ||
		*questions[0].Header != "Choose" || questions[0].MultiSelect == nil ||
		!*questions[0].MultiSelect || questions[0].Options == nil ||
		len(*questions[0].Options) != 2 ||
		(*questions[0].Options)[0].Label != "tests" {
		t.Fatalf("provider request = %#v", providerValue.requestValue)
	}
	wantText := `{"answers":[{"id":"targets","selected":["tests","docs"],"custom":"release notes"}]}`
	if got := resultText(t, outcome); got != wantText {
		t.Fatalf("rendered answer = %q", got)
	}
}

func TestExecutionPreservesStructuredQuestionError(t *testing.T) {
	t.Parallel()
	definition, err := NewDefinition(userquestions.New(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	outcome := executeQuestion(t, definition,
		context.Background(),
		toolscore.ToolExecutionInput{
			CallID:    "ask-no-provider",
			Name:      Name,
			Arguments: json.RawMessage(`{"questions":[{"id":"continue","question":"Continue?"}]}`),
		},
	)
	failure, matches := outcome.(*toolscore.ToolExecutionFailure)
	if !matches || failure.Error.Info == nil ||
		failure.Error.Info.Name != "UserQuestionError" ||
		failure.Error.Info.Code != userquestions.CodeNoProvider {
		t.Fatalf("failure = %#v", outcome)
	}
}

func executeQuestion(t *testing.T, definition toolscore.ToolDefinition, ctx context.Context, input toolscore.ToolExecutionInput) toolscore.ToolExecutionResult {
	t.Helper()
	coordinator, err := execution.NewScheduler([]toolscore.Descriptor{definition.Describe()}, questionBody{
		handler: definition.Executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := coordinator.PrepareCall(ctx, input, execution.Policies{})
	if err != nil {
		t.Fatal(err)
	}
	err = current.Dispatch()
	if err != nil {
		t.Fatal(err)
	}
	err = current.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	result, err := current.Result()
	if err != nil {
		t.Fatal(err)
	}
	return result
}
