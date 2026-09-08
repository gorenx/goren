package sessionevents

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/apiproxy/stream"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"
)

type snapshotSource struct {
	snapshot Snapshot
	reading  chan struct{}
	proceed  chan struct{}
}

func (source *snapshotSource) ListSnapshots(ctx context.Context) ([]Snapshot, error) {
	if source.reading != nil {
		close(source.reading)
		select {
		case <-source.proceed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []Snapshot{source.snapshot}, nil
}
func (source *snapshotSource) ReadSnapshot(context.Context, session.SessionID, string) (Snapshot, error) {
	return source.snapshot, nil
}
func TestFeedSubscribesBeforeSnapshotAndSuppressesLateCommit(t *testing.T) {
	hub, err := stream.NewBroker(stream.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	initial := session.Event{
		Type: "turn/start",
		Seq:  0,
		Time: 1,
		Data: json.RawMessage(`{"turn":1}`),
	}
	later := session.Event{
		Type: "turn/end",
		Seq:  1,
		Time: 2,
		Data: json.RawMessage(`{"turn":1}`),
	}
	snapshots := &snapshotSource{
		snapshot: Snapshot{
			CreationID: "creation",
			Header: session.Header{
				ID: "s",
			},
			Events: []session.Event{initial},
		},
		reading: make(chan struct{}),
		proceed: make(chan struct{}),
	}
	source, err := NewFeed(snapshots, hub)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outputs := make(chan connection.RPCRequest, 8)
	done := make(chan error, 1)
	go func() {
		done <- source.Mux(ctx, func(item connection.RPCRequest) error { outputs <- item; return nil })
	}()
	select {
	case <-snapshots.reading:
	case <-time.After(time.Second):
		t.Fatal("snapshot not read")
	}
	for _, event := range []session.Event{initial, later} {
		if err := source.Committed(context.Background(), "s", "creation", event); err != nil {
			t.Fatal(err)
		}
	}
	close(snapshots.proceed)
	for _, expected := range []string{"session/subscribed", "session/event"} {
		select {
		case item := <-outputs:
			var frame struct {
				Type    string `json:"type"`
				LastSeq int64  `json:"lastSeq"`
				Event   struct {
					Seq int64 `json:"seq"`
				} `json:"event"`
			}
			if err := json.Unmarshal(item.Payload, &frame); err != nil {
				t.Fatal(err)
			}
			if frame.Type != expected {
				t.Fatalf("frame %s, want %s", frame.Type, expected)
			}
			if frame.Type == "session/event" && frame.Event.Seq != 1 {
				t.Fatal("late callback bypassed baseline cut")
			}
		case <-time.After(time.Second):
			t.Fatal("stream result missing")
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}
