package toolactor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
	toolactor "github.com/gorenx/goren/tools/tool/actor"
	"github.com/gorenx/goren/userquestions"
)

type liveQuestionCaller struct{}

func (liveQuestionCaller) CheckCaller(context.Context, session.SessionID) (userquestions.Eligibility, error) {
	return userquestions.Eligibility{
		Live: true,
	}, nil
}

type humanQuestions struct {
	requests  chan userquestions.Request
	cancelled chan struct{}
	release   chan struct{}
}

func (service *humanQuestions) Ask(ctx context.Context, input userquestions.Request) (userquestions.Answer, error) {
	service.requests <- input
	select {
	case <-ctx.Done():
		close(service.cancelled)
		<-service.release
		return userquestions.Answer{}, context.Cause(ctx)
	case <-service.release:
		return userquestions.Answer{
			Answers: []userquestions.AnswerItem{{
				ID:       "q",
				Selected: []string{"yes"},
			}},
		}, nil
	}
}

func TestToolActorQuestionsWaitForAnswerOrCancellationCleanup(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		name := "answer"
		if cancelled {
			name = "cancel"
		}
		t.Run(name, func(t *testing.T) {
			source := &provider{}
			system, target := startTool(t, source)
			service := &humanQuestions{
				requests:  make(chan userquestions.Request, 1),
				cancelled: make(chan struct{}),
				release:   make(chan struct{}),
			}
			t.Cleanup(func() {
				select {
				case <-service.release:
				default:
					close(service.release)
				}
			})
			source.questions = userquestions.New(service, liveQuestionCaller{})
			provisionTool(t, system, target)
			type received struct {
				body tools.BodyResult
				err  error
			}
			results := make(chan received, 1)
			go func() {
				payload, err := system.AskGrain(context.Background(), target, invocation(currentIdentity(t, system, target)), 3*time.Second)
				body, valid := payload.(tools.BodyResult)
				if err == nil && !valid {
					err = errors.New("unexpected execution reply")
				}
				results <- received{
					body: body,
					err:  err,
				}
			}()
			select {
			case request := <-service.requests:
				if request.SessionID != "conversation" || len(request.Questions) != 1 || request.Questions[0].ID != "q" {
					t.Fatalf("question identity or parameters changed: %#v", request)
				}
			case <-time.After(time.Second):
				t.Fatal("question provider was not reached")
			}
			if cancelled {
				if reply := ask[toolactor.Completion](t, system, target, toolactor.CancelCall{
					CreationID: "creation",
					CallID:     "call",
				}); reply.Err != nil {
					t.Fatal(reply.Err)
				}
				select {
				case <-service.cancelled:
				case <-time.After(time.Second):
					t.Fatal("cancellation did not reach the answerer")
				}
			}
			select {
			case result := <-results:
				t.Fatalf("tool replied before question provider exit: %#v", result)
			default:
			}
			if reply := ask[toolactor.Completion](t, system, target, toolactor.Close{
				CreationID: "creation",
			}); reply.Err == nil || source.closes.Load() != 0 {
				t.Fatal("active question allowed tool resource release")
			}
			close(service.release)
			select {
			case result := <-results:
				if result.err != nil {
					t.Fatal(result.err)
				}
				if cancelled {
					if !errors.Is(result.body.Err, context.Canceled) {
						t.Fatalf("cancellation lost: %v", result.body.Err)
					}
				} else if result.body.Err != nil || string(result.body.Value) != `{"answers":[{"id":"q","selected":["yes"]}]}` {
					t.Fatalf("answer changed: %#v", result.body)
				}
			case <-time.After(time.Second):
				t.Fatal("provider exit did not complete tool call")
			}
			if reply := ask[toolactor.Completion](t, system, target, toolactor.Close{
				CreationID: "creation",
			}); reply.Err != nil || source.closes.Load() != 1 {
				t.Fatalf("tool cleanup: %v, closes = %d", reply.Err, source.closes.Load())
			}
		})
	}
}
