package projectioncache

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/session/projection"
)

type replayCounter struct{}

func (replayCounter) Key() string                                              { return "count" }
func (replayCounter) StateVersion() int64                                      { return 1 }
func (replayCounter) InitialState() (json.RawMessage, error)                   { return json.RawMessage(`0`), nil }
func (replayCounter) ViewState(state json.RawMessage) (json.RawMessage, error) { return state, nil }
func (replayCounter) ApplyState(state json.RawMessage, _ session.Event) (projection.Transition, error) {
	var count int
	if err := json.Unmarshal(state, &count); err != nil {
		return projection.Transition{}, err
	}
	encoded, err := json.Marshal(count + 1)
	return projection.Transition{
		State:   encoded,
		Changed: true,
	}, err
}

func TestReplayAccumulatesPagesWithoutMixingRevisions(t *testing.T) {
	directory := projection.NewRegistry()
	if _, err := directory.Register(replayCounter{}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewReplay("conversation", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	cwd := "/original"
	metadata := session.Header{
		ID:        "conversation",
		CreatedAt: 1,
		CWD:       &cwd,
	}
	page := persistence.EventSegment{
		Header:   metadata,
		Revision: "revision",
		Events: []session.Event{
			{
				Seq: 0,
			},
			{
				Seq: 1,
			},
		},
		HasMore: true,
	}
	if _, err := owner.Result(); err == nil {
		t.Fatal("unfinished replay reported a result")
	}
	continuation, more := owner.Next()
	if !more || continuation.FromSeq != 0 || continuation.Limit != PageSize {
		t.Fatalf("next = %#v, %v", continuation, more)
	}
	result, err := directory.Restore(owner.Checkpoint(), page.Events, continuation.FromSeq)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Accept(page, result); err != nil {
		t.Fatal(err)
	}
	result.Checkpoint["count"].Value[0] = '9'
	result.Snapshot.Values["count"][0] = '9'
	continuation, more = owner.Next()
	if !more || continuation.FromSeq != 2 {
		t.Fatalf("next = %#v, %v", continuation, more)
	}
	page.Events = []session.Event{
		{
			Seq: 2,
		},
	}
	page.HasMore = false
	page.Revision = "changed"
	if err := owner.ValidatePage(page); err == nil {
		t.Fatal("mixed durable revisions accepted")
	}
	page.Revision = "revision"
	result, err = directory.Restore(owner.Checkpoint(), page.Events, continuation.FromSeq)
	if err != nil {
		t.Fatal(err)
	}
	invalid := result
	invalid.Snapshot.AsOfSeq = 99
	if err := owner.Accept(page, invalid); err == nil {
		t.Fatal("unrelated result cut accepted")
	}
	unchanged, _ := owner.Next()
	if unchanged.FromSeq != 2 {
		t.Fatal("failed acceptance advanced cursor")
	}
	if err := owner.Accept(page, result); err != nil {
		t.Fatal(err)
	}
	cwd = "/mutated"
	view, err := owner.Result()
	if err != nil || view.Snapshot.AsOfSeq != 2 || string(view.Snapshot.Values["count"]) != "3" || *view.Header.CWD != "/original" {
		t.Fatalf("view = %#v, %v", view, err)
	}
	if _, more := owner.Next(); more {
		t.Fatal("final page did not complete replay")
	}
	view.Checkpoint["count"].Value[0] = '8'
	detached, err := owner.Result()
	if err != nil || string(detached.Checkpoint["count"].Value) != "3" {
		t.Fatal("result aliases replay state")
	}
}

func TestReplayRejectsForeignCheckpointAndInvalidPages(t *testing.T) {
	record := cacheRecord(0, "cached")
	owner, err := NewReplay("conversation", &record, 0)
	if err != nil {
		t.Fatal(err)
	}
	page := persistence.EventSegment{
		Header: session.Header{
			ID:        "conversation",
			CreatedAt: 2,
		},
		Revision: "revision",
	}
	if err := owner.ValidatePage(page); !errors.Is(err, ErrCheckpointIdentity) {
		t.Fatalf("identity error = %v", err)
	}
	owner, err = NewReplay("conversation", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	page.HasMore = true
	if err := owner.ValidatePage(page); err == nil {
		t.Fatal("empty continued page accepted")
	}
	page.HasMore = false
	page.Events = []session.Event{
		{
			Seq: 1,
		},
	}
	if err := owner.ValidatePage(page); err == nil {
		t.Fatal("missing first event accepted")
	}
	page.Events = nil
	page.Header.ID = "other"
	if err := owner.ValidatePage(page); err == nil {
		t.Fatal("foreign Session page accepted")
	}
}
