package llmactor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type clientFixture struct {
	endpoint *Client
	calls    chan any
	id       CallID
}
type initializeClient struct {
	router *goakt.PID
	calls  chan any
}
type startClient struct{}
type readClient struct{}
type closeClient struct{}

func (*clientFixture) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*clientFixture) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *clientFixture) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case initializeClient:
		endpoint, err := NewClient(input.router, 300*time.Millisecond)
		owner.endpoint = endpoint
		owner.calls = input.calls
		gc.Response(err == nil)
	case startClient:
		owner.id = owner.endpoint.NewCall()
		owner.endpoint.Start(gc, owner.id, AgentConfig{
			AgentID: "agent",
		}, llm.Input{}, owner)
		gc.Response(true)
	case readClient:
		owner.endpoint.Read(gc, owner.id, owner)
		gc.Response(true)
	case closeClient:
		owner.endpoint.Close(gc, owner.id, owner)
		gc.Response(true)
	default:
		if !owner.endpoint.Receive(gc, Receivers{
			Generation: owner,
			Chunk:      owner,
			Close:      owner,
		}) {
			gc.Unhandled()
		}
	}
}
func (owner *clientFixture) OnGenerationStarted(_ *goakt.GrainContext, result GenerationStarted) {
	owner.calls <- result
}
func (owner *clientFixture) OnChunkRead(_ *goakt.GrainContext, result ChunkRead) {
	owner.calls <- result
}
func (owner *clientFixture) OnCallClosed(_ *goakt.GrainContext, result CallClosed) {
	owner.calls <- result
}

type routerFixture struct{ messages chan any }

func (*routerFixture) PreStart(*goakt.Context) error { return nil }
func (*routerFixture) PostStop(*goakt.Context) error { return nil }
func (owner *routerFixture) Receive(rc *goakt.ReceiveContext) {
	if input, ok := rc.Message().(*goakt.Broadcast); ok {
		owner.messages <- input.Message()
	}
}

func TestClientCorrelatesRepliesAndClosesSameCallAfterReadTimeout(t *testing.T) {
	ctx := context.Background()
	system, err := goakt.NewActorSystem("client-test", goakt.WithLogger(log.DiscardLogger))
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
	messages := make(chan any, 8)
	router, err := system.Spawn(ctx, "router", &routerFixture{
		messages: messages,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := goakt.GrainOf[*clientFixture](ctx, system, "client", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	calls := make(chan any, 8)
	ask := func(input any) {
		t.Helper()
		response, err := system.AskGrain(ctx, target, input, time.Second)
		if err != nil || response != true {
			t.Fatalf("request: %v, %v", response, err)
		}
	}
	deliver := func(input any) {
		t.Helper()
		if err := system.TellGrain(ctx, target, input); err != nil {
			t.Fatal(err)
		}
	}
	receive := func(source <-chan any) any {
		t.Helper()
		select {
		case input := <-source:
			return input
		case <-time.After(2 * time.Second):
			t.Fatal("missing message")
			return nil
		}
	}
	ask(initializeClient{
		router: router,
		calls:  calls,
	})
	ask(startClient{})
	opened := receive(messages).(Generation)
	deliver(GenerationStarted{
		CallID:    "wrong",
		RequestID: opened.RequestID,
	})
	select {
	case unexpected := <-calls:
		t.Fatalf("wrong call accepted: %#v", unexpected)
	default:
	}
	deliver(GenerationStarted{
		CallID:    opened.CallID,
		RequestID: opened.RequestID,
	})
	if received := receive(calls).(GenerationStarted); received.Err != nil || received.CallID != opened.CallID {
		t.Fatalf("opening: %#v", received)
	}
	ask(readClient{})
	reading := receive(messages).(ReadChunk)
	timedOut := receive(calls).(ChunkRead)
	if !errors.Is(timedOut.Err, context.DeadlineExceeded) || timedOut.CallID != opened.CallID {
		t.Fatalf("read timeout: %#v", timedOut)
	}
	deliver(ChunkRead{
		CallID:    reading.CallID,
		RequestID: reading.RequestID,
		Found:     true,
	})
	ask(closeClient{})
	closing := receive(messages).(CloseCall)
	if closing.CallID != opened.CallID {
		t.Fatal("close changed generation identity")
	}
	deliver(CallClosed{
		CallID:    closing.CallID,
		RequestID: closing.RequestID,
	})
	if received, ok := receive(calls).(CallClosed); !ok || received.Err != nil {
		t.Fatalf("late read interfered with close: %#v", received)
	}
}
