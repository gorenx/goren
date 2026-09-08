package userquestions

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/session"
)

type callerFixture struct{ eligibility Eligibility }

func (source callerFixture) CheckCaller(context.Context, session.SessionID) (Eligibility, error) {
	return source.eligibility, nil
}

type providerFixture struct {
	request Request
	answer  Answer
	cancel  context.CancelFunc
}

func (source *providerFixture) Ask(_ context.Context, input Request) (Answer, error) {
	source.request = input
	if source.cancel != nil {
		source.cancel()
	}
	input.Questions[0].Question = "provider mutation"
	return source.answer, nil
}

func TestAdmissionAndIntentErrors(t *testing.T) {
	detail := "Review the plan"
	for _, scenario := range []struct {
		name        string
		eligibility Eligibility
		questions   []Question
		code        string
	}{
		{
			name: "missing caller",
			code: CodeCallerNotLive,
			questions: []Question{{
				ID:       "q",
				Question: "Continue?",
			}},
		},
		{
			name: "delegated",
			eligibility: Eligibility{
				Live:      true,
				Delegated: true,
			},
			code: CodeDelegatedCaller,
			questions: []Question{{
				ID:       "q",
				Question: "Continue?",
			}},
		},
		{
			name: "empty",
			eligibility: Eligibility{
				Live: true,
			},
			code: CodeEmptyQuestions,
		},
		{
			name: "bad intent",
			eligibility: Eligibility{
				Live: true,
			},
			code: CodeBadIntent,
			questions: []Question{{
				ID: "q",
				Intent: &Intent{
					Kind: "unknown",
				},
			}},
		},
		{
			name: "no approve option",
			eligibility: Eligibility{
				Live: true,
			},
			code: CodeBadIntent,
			questions: []Question{{
				ID:     "q",
				Detail: &detail,
				Intent: &Intent{
					Kind:    IntentPlanReview,
					Approve: "yes",
				},
			}},
		},
		{
			name: "no provider",
			eligibility: Eligibility{
				Live: true,
			},
			code: CodeNoProvider,
			questions: []Question{{
				ID:       "q",
				Question: "Continue?",
			}},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			service := New(nil, callerFixture{
				eligibility: scenario.eligibility,
			})
			_, err := service.Ask(context.Background(), Request{
				SessionID: "s",
				Questions: scenario.questions,
			})
			var problem *Error
			if !errors.As(err, &problem) || problem.Code != scenario.code {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestAskDetachesInputsAndAnswers(t *testing.T) {
	provider := &providerFixture{
		answer: Answer{
			Answers: []AnswerItem{{
				ID:       "q",
				Selected: []string{"yes"},
			}},
		},
	}
	service := New(provider, callerFixture{
		eligibility: Eligibility{
			Live: true,
		},
	})
	input := Request{
		SessionID: "s",
		Questions: []Question{{
			ID:       "q",
			Question: "Continue?",
		}},
	}
	answerValue, err := service.Ask(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if input.Questions[0].Question != "Continue?" || provider.request.SessionID != "s" {
		t.Fatal("request alias or lost caller")
	}
	answerValue.Answers[0].Selected[0] = "changed"
	if provider.answer.Answers[0].Selected[0] != "yes" {
		t.Fatal("answer aliases provider")
	}
}

func TestCancellationWinsOverLateAnswer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	provider := &providerFixture{
		cancel: cancel,
	}
	service := New(provider, nil)
	_, err := service.Ask(ctx, Request{
		Questions: []Question{{
			ID:       "q",
			Question: "Continue?",
		}},
	})
	var problem *Error
	if !errors.Is(err, context.Canceled) || !errors.As(err, &problem) || problem.Code != CodeAborted {
		t.Fatalf("error = %v", err)
	}
}
