package toolaskuser

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/tools/execution"
	"github.com/gorenx/goren/userquestions"
)

type liveCaller struct{}

func (liveCaller) CheckCaller(context.Context, session.SessionID) (userquestions.Eligibility, error) {
	return userquestions.Eligibility{
		Live: true,
	}, nil
}

func TestToolExecutionUsesQuestionServiceAndSessionIdentity(t *testing.T) {
	ctx := context.Background()
	provider := &providerRecorder{
		answerValue: userquestions.Answer{
			Answers: []userquestions.AnswerItem{{
				ID:       "q",
				Selected: []string{"yes"},
			}},
		},
	}
	endpoint := userquestions.New(provider, liveCaller{})
	definition, err := NewDefinition(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := execution.NewScheduler([]tools.Descriptor{definition.Describe()}, questionBody{
		handler: definition.Executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := coordinator.PrepareCall(ctx, tools.ToolExecutionInput{
		SessionID: "session-1",
		CallID:    "ask-1",
		Name:      Name,
		Arguments: json.RawMessage(`{"questions":[{"id":"q","question":"Continue?"}]}`),
	}, execution.Policies{},
	)
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
	outcome, err := current.Result()
	if err != nil {
		t.Fatal(err)
	}
	if provider.requestValue.SessionID != "session-1" {
		t.Fatalf("caller = %q", provider.requestValue.SessionID)
	}
	if got := resultText(t, outcome); got != `{"answers":[{"id":"q","selected":["yes"]}]}` {
		t.Fatalf("result = %s", got)
	}
}

type questionBody struct{ handler tools.Executor }

func (endpoint questionBody) Invoke(ctx context.Context, input tools.Invocation) (tools.BodyResult, error) {
	return input.Run(ctx, endpoint.handler), nil
}
