package toolaskuser

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/userquestions"
	questionactor "github.com/gorenx/goren/userquestions/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type liveCaller struct{}

func (liveCaller) CheckCaller(context.Context, session.SessionID) (userquestions.Eligibility, error) {
	return userquestions.Eligibility{
		Live: true,
	}, nil
}

func TestToolExecutionUsesQuestionActorAndSessionIdentity(t *testing.T) {
	ctx := context.Background()
	system, err := goakt.NewActorSystem("ask-tool-test", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := system.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	target, err := goakt.GrainOf[*questionactor.Grain](ctx, system, "session-1", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	provider := &providerRecorder{
		answerValue: userquestions.Answer{
			Answers: []userquestions.AnswerItem{{
				ID:       "q",
				Selected: []string{"yes"},
			}},
		},
	}
	payload, err := system.AskGrain(ctx, target, questionactor.Initialize{
		Provider: provider,
		Callers:  liveCaller{},
	}, time.Second)
	if err != nil || payload.(questionactor.Completion).Err != nil {
		t.Fatalf("initialize: %v %v", payload, err)
	}
	endpoint, err := questionactor.NewClient(system, target)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := NewDefinition(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	config, err := tools.ValidateConfig(tools.Config{})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := tools.New(config, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(ctx)
	if _, err := runtime.AddTool(ctx, definition); err != nil {
		t.Fatal(err)
	}
	outcome := runtime.Execute(ctx, tools.ToolExecutionInput{
		SessionID: "session-1",
		CallID:    "ask-1",
		Name:      Name,
		Arguments: json.RawMessage(`{"questions":[{"id":"q","question":"Continue?"}]}`),
	})
	if provider.requestValue.SessionID != "session-1" {
		t.Fatalf("caller = %q", provider.requestValue.SessionID)
	}
	if got := resultText(t, outcome); got != `{"answers":[{"id":"q","selected":["yes"]}]}` {
		t.Fatalf("result = %s", got)
	}
}
