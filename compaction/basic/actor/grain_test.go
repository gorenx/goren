package compactionactor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/compaction/basic"
	"github.com/gorenx/goren/llm"
	llmactor "github.com/gorenx/goren/llm/actor"
	meteractor "github.com/gorenx/goren/llm/tokenmeter/actor"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type historyFixture struct {
	history  *session.Log
	flushing chan *goakt.GrainReply
}
type bindHistory struct {
	history  *session.Log
	flushing chan *goakt.GrainReply
}

func (*historyFixture) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*historyFixture) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *historyFixture) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case bindHistory:
		owner.history = input.history
		owner.flushing = input.flushing
		gc.Response(true)
	case sessionactor.Read:
		gc.Response(sessionactor.View{
			Snapshot: owner.history.Snapshot(),
		})
	case sessionactor.Commit:
		written, err := owner.history.Commit(gc.Context(), input.Plan)
		gc.Response(sessionactor.Committed{
			Result: written,
			Err:    err,
		})
	case sessionactor.Flush:
		owner.flushing <- gc.DeferResponse()
	}
}

type modelFixture struct {
	reads int
	calls chan string
}

func (*modelFixture) PreStart(*goakt.Context) error { return nil }
func (*modelFixture) PostStop(*goakt.Context) error { return nil }
func (owner *modelFixture) Receive(rc *goakt.ReceiveContext) {
	envelope, ok := rc.Message().(*goakt.Broadcast)
	if !ok {
		return
	}
	var route llmactor.Delivery
	var result any
	switch input := envelope.Message().(type) {
	case llmactor.Generation:
		route = input.Delivery
		owner.reads = 0
		owner.calls <- "generation"
		result = llmactor.GenerationStarted{
			CallID:    route.CallID,
			RequestID: route.RequestID,
			Config:    llm.CallConfig(input.Settings.(llmactor.Parameters)),
		}
	case llmactor.ReadChunk:
		route = input.Delivery
		owner.reads++
		result = llmactor.ChunkRead{
			CallID:    route.CallID,
			RequestID: route.RequestID,
			Found:     owner.reads == 1,
			Chunk: llm.TextDeltaChunk{
				Index: 0,
				Text:  "checkpoint",
			},
		}
	case llmactor.CloseCall:
		route = input.Delivery
		owner.calls <- "close"
		result = llmactor.CallClosed{
			CallID:    route.CallID,
			RequestID: route.RequestID,
		}
	default:
		return
	}
	rc.RequestGrain(route.ReplyTo, result).Then(func(any, error) {})
}

func TestGrainWaitsForFlushAndSettlesPersistenceFailure(t *testing.T) {
	for _, fails := range []bool{false, true} {
		name := "success"
		if fails {
			name = "persistence failure"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			system, err := goakt.NewActorSystem("compaction-test", goakt.WithLogger(log.DiscardLogger))
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
			history, err := session.NewLog("conversation", session.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for range 3 {
				entry, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
					Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock(strings.Repeat("old history ", 1000))},
					Source:  agentmessage.UserMessageSource{},
				})
				if err != nil {
					t.Fatal(err)
				}
				draft, err := session.NewSurfaceEventDraft(session.UserMessageAdded, entry, session.SurfaceIntent{
					Operation: session.SurfaceAppend(),
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := history.Commit(ctx, session.Batch(draft)); err != nil {
					t.Fatal(err)
				}
			}
			conversation, err := goakt.GrainOf[*historyFixture](ctx, system, "conversation", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			flushing := make(chan *goakt.GrainReply, 1)
			if _, err := system.AskGrain(ctx, conversation, bindHistory{
				history:  history,
				flushing: flushing,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			meter, err := goakt.GrainOf[*meteractor.Grain](ctx, system, "conversation", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			calls := make(chan string, 8)
			router, err := system.Spawn(ctx, "model", &modelFixture{
				calls: calls,
			}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
			if err != nil {
				t.Fatal(err)
			}
			target, err := goakt.GrainOf[*Grain](ctx, system, "conversation", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			response, err := system.AskGrain(ctx, target, Initialize{
				AgentID:    "conversation",
				Session:    conversation,
				CreationID: "creation",
				Meter:      meter,
				Router:     router,
				Timeout:    time.Second,
			}, time.Second)
			if err != nil || response.(Completion).Err != nil {
				t.Fatalf("initialize: %#v, %v", response, err)
			}
			done := make(chan Result, 1)
			failures := make(chan error, 1)
			go func() {
				response, err := system.AskGrain(ctx, target, Execute{
					Mode: basic.Manual,
					Fallback: basic.RouteTarget{
						Provider: "summary",
						Model:    "checkpoint",
					},
				}, 5*time.Second)
				if err != nil {
					failures <- err
					return
				}
				done <- response.(Result)
			}()
			var reply *goakt.GrainReply
			select {
			case reply = <-flushing:
			case err := <-failures:
				t.Fatal(err)
			case result := <-done:
				t.Fatalf("finished before flush: %#v", result)
			case <-time.After(4 * time.Second):
				t.Fatal("flush not reached")
			}
			if first, second := <-calls, <-calls; first != "generation" || second != "close" {
				t.Fatalf("model lifecycle = %s, %s", first, second)
			}
			select {
			case result := <-done:
				t.Fatalf("early reply: %#v", result)
			default:
			}
			response, err = system.AskGrain(ctx, conversation, sessionactor.Read{}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := response.(sessionactor.View).Snapshot
			state, err := compaction.InspectLog(snapshot.Events)
			if err != nil || state.Attempt != nil {
				t.Fatalf("unclosed end: %#v, %v", state, err)
			}
			var diskFailure error
			if fails {
				diskFailure = errors.New("disk full")
			}
			reply.Response(sessionactor.Flushed{
				Barrier: snapshot.Barrier,
				Err:     diskFailure,
			})
			select {
			case result := <-done:
				if !result.Outcome.Changed {
					t.Fatal("lost committed progress")
				}
				if fails {
					var manual *compaction.ManualError
					if !errors.As(result.Err, &manual) || manual.Code != compaction.ManualErrorPersistence || !errors.Is(result.Err, diskFailure) {
						t.Fatalf("failure = %v", result.Err)
					}
				} else if result.Err != nil || result.Outcome.Latest == nil {
					t.Fatalf("result = %#v", result)
				}
			case err := <-failures:
				t.Fatal(err)
			case <-time.After(2 * time.Second):
				t.Fatal("flush did not settle invocation")
			}
			response, err = system.AskGrain(ctx, target, Close{}, time.Second)
			if err != nil || response.(Completion).Err != nil {
				t.Fatalf("close: %#v, %v", response, err)
			}
		})
	}
}
