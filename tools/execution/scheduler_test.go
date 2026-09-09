package execution

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/toolaskuser"
	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/userquestions"
)

type bodyEndpoint struct {
	result  tools.BodyResult
	failure error
	calls   int
	panics  bool
}

func (endpoint *bodyEndpoint) Invoke(context.Context, tools.Invocation) (tools.BodyResult, error) {
	endpoint.calls++
	if endpoint.panics {
		panic("delivery failed after submission")
	}
	return endpoint.result, endpoint.failure
}

func TestSchedulerDistinguishesUnconfirmedInvocationFromBodyFailure(t *testing.T) {
	failure := errors.New("request interrupted")
	for _, name := range []string{"body-failure", "delivery-unconfirmed", "delivery-panic"} {
		unconfirmed := name != "body-failure"
		endpoint := &bodyEndpoint{
			result: tools.BodyResult{
				Err: failure,
			},
		}
		if unconfirmed {
			endpoint.failure = failure
			endpoint.panics = name == "delivery-panic"
		}
		t.Run(name, func(t *testing.T) {
			definition, err := toolaskuser.NewDefinition(schedulerQuestions{})
			if err != nil {
				t.Fatal(err)
			}
			coordinator, err := NewScheduler([]tools.Descriptor{definition.Describe()}, Policies{}, endpoint)
			if err != nil {
				t.Fatal(err)
			}
			// Unknown tool is rejected before body delivery, even if the endpoint would fail.
			prepared, err := coordinator.prepare(context.Background(), tools.ToolExecutionInput{
				CallID:    "missing-call",
				Name:      "missing",
				SessionID: "conversation",
				Arguments: []byte(`{}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			dispatched, err := coordinator.dispatch(prepared.Execution)
			if err != nil || dispatched.Result == nil || !dispatched.Result.Failed() || endpoint.calls != 0 {
				t.Fatalf("unknown tool reached endpoint: %+v, %v", dispatched, err)
			}
			prepared, err = coordinator.prepare(context.Background(), tools.ToolExecutionInput{
				CallID:    "actual-call",
				Name:      toolaskuser.Name,
				SessionID: "conversation",
				Arguments: []byte(`{"questions":[{"id":"q","question":"Continue?"}]}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			dispatched, err = coordinator.dispatch(prepared.Execution)
			if endpoint.calls != 1 {
				t.Fatal("valid call was not delivered")
			}
			if unconfirmed {
				var delivery *DeliveryError
				if !errors.As(err, &delivery) || (name != "delivery-panic" && !errors.Is(err, failure)) || dispatched.Result != nil {
					t.Fatalf("unconfirmed delivery was converted to a tool result: %+v, %v", dispatched, err)
				}
			} else if err != nil || dispatched.Result == nil || !dispatched.Result.Failed() {
				t.Fatalf("returned body failure lost its business result: %+v, %v", dispatched, err)
			}

			if unconfirmed {
				current, failure := coordinator.PrepareCall(context.Background(), tools.ToolExecutionInput{
					CallID:    "owned-call",
					Name:      toolaskuser.Name,
					SessionID: "conversation",
					Arguments: []byte(`{"questions":[{"id":"q","question":"Continue?"}]}`),
				})
				if failure != nil {
					t.Fatal(failure)
				}
				failure = current.Dispatch()
				var delivery *DeliveryError
				if !errors.As(failure, &delivery) {
					t.Fatal("Call lost uncertain delivery", failure)
				}
				if current.Finalize() == nil {
					t.Fatal("Call finalized without body completion")
				}
				if result, failure := current.Result(); failure == nil || result != nil {
					t.Fatal("Call fabricated a final result")
				}
				calls := endpoint.calls
				if current.Dispatch() == nil || endpoint.calls != calls {
					t.Fatal("uncertain body was automatically resubmitted")
				}
			}

		})
	}
}

type schedulerQuestions struct{}

func (schedulerQuestions) Ask(context.Context, userquestions.Request) (userquestions.Answer, error) {
	return userquestions.Answer{}, nil
}
