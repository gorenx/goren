package apiproxy

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"
)

func TestProjectQueueFoldsDurableSplicesAndUserPlacement(t *testing.T) {
	t.Parallel()
	origin, err := agentmessage.NewOpaqueMessageSource("user", json.RawMessage(`{"kind":"user","rpcId":"rpc-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	messageValue, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("queued")}, Source: origin,
	})
	if err != nil {
		t.Fatal(err)
	}
	mutation := agent.InboxSplice{Target: agent.NextStep, Start: 0, Inserted: []agentmessage.UserMessage{messageValue}}
	encoded, err := json.Marshal(mutation)
	if err != nil {
		t.Fatal(err)
	}
	items, err := projectQueue(session.Header{}, []session.Event{{
		Type: "agent/inbox/spliced", Seq: 0, Time: 1, Data: encoded,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Placement != QueueSteering || items[0].ID != MessageID(messageValue.StableID()) {
		t.Fatalf("items = %#v", items)
	}
}

func TestMuxBaselineHighwaterSuppressesLateCommittedCallback(t *testing.T) {
	conversation, err := session.New("session-1", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	committed, err := session.Event{}, error(nil)
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewEventDraft(session.TurnStarted, session.TurnStart{Turn: 1})
		writeErr = draftErr
		if draftErr == nil {
			receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
			writeErr = commitErr
			if commitErr == nil {
				committedEvent = receipt.Events[0]
			}
		}
		committed = committedEvent
		err = writeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	nextRPC := 0
	hub := newLiveFrameHub(func() (connection.RPCID, error) {
		nextRPC++
		return connection.RPCID("rpc-" + string(rune('0'+nextRPC))), nil
	})
	streamContext, cancelStream := context.WithCancel(context.Background())
	received := make(chan StreamRequest[MuxFrame], 4)
	done := make(chan error, 1)
	go func() {
		done <- hub.openMux(streamContext, []session.Context{conversation}, func(item StreamRequest[MuxFrame]) error {
			received <- item
			return nil
		})
	}()
	select {
	case firstFrame := <-received:
		subscribed, matched := firstFrame.Payload.(SessionSubscribedFrame)
		if !matched || subscribed.LastSeq != 0 {
			t.Fatalf("baseline = %#v", firstFrame)
		}
	case <-time.After(time.Second):
		t.Fatal("baseline timed out")
	}
	projected, err := ProjectSessionEvent(committed)
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.sessionEvent(conversation.ID(), projected, false, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case duplicate := <-received:
		t.Fatalf("late callback duplicated baseline event: %#v", duplicate)
	case <-time.After(20 * time.Millisecond):
	}
	cancelStream()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSessionFrameHubSurfacesMintFailureWithoutAdvancingHighwater(t *testing.T) {
	t.Parallel()
	mintFailure := errors.New("entropy unavailable")
	hub := newLiveFrameHub(func() (connection.RPCID, error) {
		return "", mintFailure
	})
	subscriber := &muxSubscriber{
		queue: newStreamDeliveryQueue[StreamRequest[MuxFrame]](),
		highwater: map[session.SessionID]int64{
			"session-1": -1,
		},
	}
	hub.mux[subscriber] = struct{}{}
	err := hub.sessionEvent("session-1", SessionEvent{Type: "turn/start", Seq: 0}, false, nil)
	if !errors.Is(err, mintFailure) {
		t.Fatalf("frame error = %v, want wrapped mint failure", err)
	}
	if subscriber.highwater["session-1"] != -1 {
		t.Fatalf("highwater = %d, want -1", subscriber.highwater["session-1"])
	}
}
