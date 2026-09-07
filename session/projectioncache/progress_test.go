package projectioncache

import (
	"testing"
	"time"

	"github.com/gorenx/goren/session"
)

func TestProgressTriggersAndPreservesUnconfirmedInput(t *testing.T) {
	now := time.Unix(100, 0)
	owner, err := NewProgress(Config{
		WriteEveryEvents: 2,
		WriteInterval:    time.Second,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Observe(session.Event{
		Seq: 0,
	}, now); err != nil {
		t.Fatal(err)
	}
	if owner.Due(now) {
		t.Fatal("single event reached count threshold")
	}
	if !owner.Due(now.Add(time.Second)) {
		t.Fatal("dirty deadline did not trigger")
	}
	if err := owner.Observe(session.Event{
		Seq: 0,
	}, now); err != nil {
		t.Fatal(err)
	}
	if owner.Due(now) {
		t.Fatal("duplicate event incremented count")
	}
	if err := owner.Observe(session.Event{
		Seq: 1,
	}, now); err != nil {
		t.Fatal(err)
	}
	if !owner.Due(now) {
		t.Fatal("event threshold did not trigger")
	}
	// A failed write leaves the original progress intact; there is no failure callback.
	if !owner.Due(now) {
		t.Fatal("unconfirmed input was lost")
	}
	if err := owner.Observe(session.Event{
		Seq:  2,
		Type: session.TurnEndEventName,
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := owner.Confirm(2); err != nil {
		t.Fatal(err)
	}
	if !owner.Due(now) {
		t.Fatal("confirmation cleared a later turn boundary")
	}
	if err := owner.Confirm(3); err != nil {
		t.Fatal(err)
	}
	if owner.Due(now.Add(time.Hour)) {
		t.Fatal("confirmed prefix stayed dirty")
	}
	if _, exists := owner.Deadline(); exists {
		t.Fatal("clean progress retained timer deadline")
	}
	if err := owner.Confirm(2); err == nil {
		t.Fatal("old confirmation rolled back progress")
	}
}

func TestProgressAllowsCaptureAheadOfNotificationConsumption(t *testing.T) {
	owner, err := NewProgress(Config{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Confirm(10); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	if err := owner.Observe(session.Event{
		Seq:  9,
		Type: session.TurnEndEventName,
	}, now); err != nil {
		t.Fatal(err)
	}
	if owner.Due(now.Add(time.Hour)) {
		t.Fatal("already checkpointed late event became dirty")
	}
	if err := owner.Observe(session.Event{
		Seq: 10,
	}, now); err != nil {
		t.Fatal(err)
	}
	deadline, exists := owner.Deadline()
	if !exists || !deadline.Equal(now.Add(DefaultWriteInterval)) {
		t.Fatalf("deadline = %v, %v", deadline, exists)
	}
}
