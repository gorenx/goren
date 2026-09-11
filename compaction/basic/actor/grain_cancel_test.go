package compactionactor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm"
	llmactor "github.com/gorenx/goren/llm/actor"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type cancellableModel struct {
	observed chan string
	reading  llmactor.Delivery
	closing  llmactor.Delivery
}
type releaseModel struct{}

func (*cancellableModel) PreStart(*goakt.Context) error { return nil }
func (*cancellableModel) PostStop(*goakt.Context) error { return nil }
func (owner *cancellableModel) Receive(rc *goakt.ReceiveContext) {
	send := func(route llmactor.Delivery, message any) {
		rc.RequestGrain(route.ReplyTo, message).Then(func(any, error) {})
	}
	if _, ok := rc.Message().(releaseModel); ok {
		send(owner.closing, llmactor.CallClosed{
			CallID:    owner.closing.CallID,
			RequestID: owner.closing.RequestID,
		})
		return
	}
	envelope, ok := rc.Message().(*goakt.Broadcast)
	if !ok {
		return
	}
	switch input := envelope.Message().(type) {
	case llmactor.Generation:
		send(input.Delivery, llmactor.GenerationStarted{
			CallID:    input.CallID,
			RequestID: input.RequestID,
			Config:    llm.CallConfig(input.Settings.(llmactor.Parameters)),
		})
	case llmactor.ReadChunk:
		owner.reading = input.Delivery
		owner.observed <- "reading"
	case llmactor.CancelCall:
		owner.observed <- "cancelled"
		send(input.Delivery, llmactor.CancellationAccepted{
			CallID:    input.CallID,
			RequestID: input.RequestID,
		})
		send(owner.reading, llmactor.ChunkRead{
			CallID:    owner.reading.CallID,
			RequestID: owner.reading.RequestID,
			Err:       input.Cause,
		})
	case llmactor.CloseCall:
		owner.closing = input.Delivery
		owner.observed <- "closing"
	}
}

func TestHookCancellationWaitsForModelCloseAndFailedEnd(t *testing.T) {
	observed := make(chan string, 8)
	system, target, coordinator, conversation, _ := hookFixture(t, true, &cancellableModel{
		observed: observed,
	})
	ctx := context.Background()
	received := make(chan agent.RequestErrorResolved, 1)
	failed := make(chan error, 1)
	go func() {
		response, err := system.AskGrain(ctx, coordinator, middlewareactor.ResolveRequestError{
			CallID: "recovery",
			Routes: []middlewareactor.Route{{
				Definition: "compaction",
				Target:     target,
			}},
			Input: agent.RequestFailure{
				AgentID: "conversation",
				Turn:    1,
				Step:    1,
				Failure: llm.LlmFailure{
					Code: llm.ContextWindowExceededCode,
				},
			},
		}, 4*time.Second)
		if err != nil {
			failed <- err
			return
		}
		received <- response.(agent.RequestErrorResolved)
	}()
	expect := func(want string) {
		t.Helper()
		select {
		case got := <-observed:
			if got != want {
				t.Fatalf("got %s, want %s", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("missing %s", want)
		}
	}
	expect("reading")
	cause := errors.New("user cancelled")
	// A stale invocation identity is acknowledged without touching the live call.
	if _, err := system.AskGrain(ctx, target, middlewareactor.CancelInvocation{
		CallID:  "old-recovery",
		Handler: "compaction",
		Cause:   cause,
	}, time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-observed:
		t.Fatalf("stale cancellation reached model: %s", got)
	default:
	}
	response, err := system.AskGrain(ctx, coordinator, middlewareactor.CancelCall{
		CallID: "recovery",
		Cause:  cause,
	}, time.Second)
	if err != nil || response.(middlewareactor.Completion).Err != nil {
		t.Fatalf("cancel: %#v, %v", response, err)
	}
	expect("cancelled")
	expect("closing")
	select {
	case result := <-received:
		t.Fatalf("replied before close: %#v", result)
	case err := <-failed:
		t.Fatal(err)
	default:
	}
	response, err = system.AskGrain(ctx, conversation, sessionactor.Read{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	state, err := compaction.InspectLog(response.(sessionactor.View).Snapshot.Events)
	if err != nil || state.Attempt == nil {
		t.Fatalf("transaction ended before resource close: %#v, %v", state, err)
	}
	provider, err := system.ActorOf(ctx, "model")
	if err != nil {
		t.Fatal(err)
	}
	if err := goakt.Tell(ctx, provider, releaseModel{}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-received:
		if result.Action.Retry || !errors.Is(result.Err, cause) {
			t.Fatalf("cancelled recovery: %#v", result)
		}
	case err := <-failed:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("original recovery did not finish")
	}
	response, err = system.AskGrain(ctx, conversation, sessionactor.Read{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := response.(sessionactor.View).Snapshot
	state, err = compaction.InspectLog(snapshot.Events)
	if err != nil || state.Attempt != nil || snapshot.Surface.ReplaceGeneration != 0 {
		t.Fatalf("cancelled transaction: %#v, %v", state, err)
	}
	last := snapshot.Events[len(snapshot.Events)-1]
	if last.Type != compaction.EndEventName {
		t.Fatalf("last event: %s", last.Type)
	}
	ending, err := compaction.DecodeEnd(last.Data)
	if err != nil || ending.Error == nil {
		t.Fatalf("missing cancellation end: %#v, %v", ending, err)
	}
}
