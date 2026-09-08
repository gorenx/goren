package stream_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/apiproxy/interaction"
	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/apiproxy/stream"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/connection"
)

func nextBatch(t *testing.T, source *stream.Subscription) stream.Batch {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	batch, found, err := source.Next(ctx)
	if err != nil || !found {
		t.Fatalf("next batch: %v %v", found, err)
	}
	return batch
}
func TestBrokerReplaysInteractionsAcrossDisconnect(t *testing.T) {
	hub, err := stream.NewBroker(stream.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	methods := rpc.NewCatalog()
	owner, err := interaction.NewGateway(interaction.Dependencies{
		Methods: methods,
		Frames:  hub,
	}, interaction.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close(context.Background())
	first, err := hub.OpenMux()
	if err != nil {
		t.Fatal(err)
	}
	decision := make(chan approval.Outcome, 1)
	go func() {
		result, _ := owner.Answer(context.Background(), approval.Question{
			ID:        "audit-1",
			SessionID: "session-1",
			ToolName:  "bash",
		})
		decision <- result
	}()
	request := nextBatch(t, first)[0]
	hub.Release(first)
	reconnected, err := hub.OpenMux()
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Release(reconnected)
	replay := reconnected.Replay()
	if len(replay) != 1 || replay[0].RPCID != request.RPCID || string(replay[0].Payload) != string(request.Payload) {
		t.Fatal("reconnect lost original pending request")
	}
	replay[0].Payload[0] = '!'
	if reconnected.Replay()[0].Payload[0] != '{' {
		t.Fatal("replay leaked mutable payload")
	}
	receipt, err := methods.Respond(context.Background(), connection.ClientResponse{
		RPCID: request.RPCID,
		Result: connection.RPCResult{
			OK:    true,
			Value: []byte(`{"sessionId":"session-1","approvalId":"audit-1","outcome":"allowed-once"}`),
		},
	})
	if err != nil || !receipt.Accepted {
		t.Fatalf("answer: %+v %v", receipt, err)
	}
	select {
	case result := <-decision:
		if result != approval.OutcomeAllowedOnce {
			t.Fatal(result)
		}
	case <-time.After(time.Second):
		t.Fatal("answer did not settle")
	}
	resolved := nextBatch(t, reconnected)[0]
	if resolved.RPCID == request.RPCID {
		t.Fatal("terminal publication reused request RPCID")
	}
	third, err := hub.OpenMux()
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Release(third)
	if len(third.Replay()) != 0 {
		t.Fatal("resolved request remained in reconnect baseline")
	}
}
func TestBrokerIsolatesSlowSubscribersAndPayloadMutation(t *testing.T) {
	hub, err := stream.NewBroker(stream.Options{
		Capacity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	slow, _ := hub.OpenMux()
	fast, _ := hub.OpenMux()
	defer hub.Release(fast)
	if err := hub.PublishMux(emptyFrame{}); err != nil {
		t.Fatal(err)
	}
	first := nextBatch(t, fast)
	first[0].Payload[0] = '!'
	if err := hub.PublishMux(emptyFrame{}); !errors.Is(err, stream.ErrSlowConsumer) {
		t.Fatalf("overflow: %v", err)
	}
	if nextBatch(t, fast)[0].Payload[0] != '{' {
		t.Fatal("subscriber changed shared publication")
	}
	if nextBatch(t, slow)[0].Payload[0] != '{' {
		t.Fatal("subscriber changed another connection")
	}
	if _, found, err := slow.Next(context.Background()); found || !errors.Is(err, stream.ErrSlowConsumer) {
		t.Fatalf("slow terminal: %v %v", found, err)
	}
	// A disconnected client cannot turn a pending business request into a failure.
	if err := hub.PublishPending("question-1", interaction.QuestionRequestedFrame{
		SessionID: "s",
		Questions: []interaction.AskUserQuestionItem{{
			ID:       "q",
			Question: "Continue?",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	nextBatch(t, fast)
	reconnected, _ := hub.OpenMux()
	defer hub.Release(reconnected)
	if len(reconnected.Replay()) != 1 {
		t.Fatal("slow consumer lost replayable question")
	}
}
func TestBrokerPublishesBatchAtomicallyOnIdentityFailure(t *testing.T) {
	calls := 0
	problem := errors.New("identity failure")
	hub, err := stream.NewBroker(stream.Options{
		NewRPCID: func() (connection.RPCID, error) {
			calls++
			if calls == 2 {
				return "", problem
			}
			return "id", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	source, _ := hub.OpenMux()
	if err := hub.PublishMuxBatch([]stream.MuxFrame{emptyFrame{}, emptyFrame{}}); !errors.Is(err, problem) {
		t.Fatalf("batch failure: %v", err)
	}
	hub.Close()
	if _, found, err := source.Next(context.Background()); found || err != nil {
		t.Fatalf("partial batch escaped: %v %v", found, err)
	}
	if _, err := hub.OpenMux(); !errors.Is(err, stream.ErrClosed) {
		t.Fatal("closed broker admitted subscriber")
	}
}
