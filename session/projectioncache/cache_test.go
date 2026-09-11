package projectioncache

import (
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/projection"
)

func cacheRecord(seq int64, value string) CheckpointRecord {
	encoded, _ := json.Marshal(value)
	return CheckpointRecord{
		Identity: LogIdentity{
			CreatedAt: 1,
		},
		Rows: projection.Checkpoint{
			"value": {
				Version: 1,
				Seq:     seq,
				Value:   encoded,
			},
		},
	}
}

func TestCachePublishesOnlyConfirmedRecords(t *testing.T) {
	var owner Cache
	initial := cacheRecord(1, "initial")
	if err := owner.Confirm("conversation", initial); err != nil {
		t.Fatal(err)
	}
	replacement, err := owner.Prepare("conversation", cacheRecord(2, "replacement"))
	if err != nil || replacement == nil {
		t.Fatalf("prepare = %#v, %v", replacement, err)
	}
	stillConfirmed, found := owner.Snapshot("conversation")
	if !found || string(stillConfirmed.Rows["value"].Value) != `"initial"` {
		t.Fatal("prepare published before storage acknowledgement")
	}
	// Omitting Confirm models a failed storage operation; retry starts from the same record.
	retry, err := owner.Prepare("conversation", cacheRecord(2, "replacement"))
	if err != nil || retry == nil {
		t.Fatal("failed write suppressed retry")
	}
	if err := owner.Confirm("conversation", *retry); err != nil {
		t.Fatal(err)
	}
	retry.Rows["value"].Value[1] = '!'
	confirmed, found := owner.Snapshot("conversation")
	if !found || string(confirmed.Rows["value"].Value) != `"replacement"` {
		t.Fatal("confirmed record aliases acknowledgement")
	}
	if err := owner.Confirm("conversation", initial); err == nil {
		t.Fatal("stale acknowledgement rolled back progress")
	}
	covered, err := owner.Prepare("conversation", initial)
	if err != nil || covered != nil {
		t.Fatalf("covered record = %#v, %v", covered, err)
	}
	confirmed.Rows["value"].Value[1] = '!'
	detached, _ := owner.Snapshot("conversation")
	if string(detached.Rows["value"].Value) != `"replacement"` {
		t.Fatal("snapshot aliases confirmed cache")
	}
}

func TestCacheRejectsForeignLogAndMalformedReplacement(t *testing.T) {
	var owner Cache
	if err := owner.Confirm("conversation", cacheRecord(0, "first")); err != nil {
		t.Fatal(err)
	}
	metadata := session.Header{
		ID:        "conversation",
		CreatedAt: 2,
	}
	if _, found := owner.Match(metadata); found {
		t.Fatal("different log reused cached rows")
	}
	invalid := cacheRecord(1, "invalid")
	invalid.Rows["value"] = projection.CheckpointRow{
		Version: 1,
		Seq:     1,
		Value:   json.RawMessage(`invalid`),
	}
	if _, err := owner.Prepare("conversation", invalid); err == nil {
		t.Fatal("malformed record prepared")
	}
	if err := owner.Confirm("conversation", invalid); err == nil {
		t.Fatal("malformed record installed")
	}
	previous, _ := owner.Snapshot("conversation")
	if previous.Rows["value"].Seq != 0 {
		t.Fatal("malformed input changed cache")
	}
	replacement := cacheRecord(0, "new-log")
	replacement.Identity.CreatedAt = 2
	if err := owner.Confirm("conversation", replacement); err != nil {
		t.Fatal(err)
	}
	if _, found := owner.Match(metadata); !found {
		t.Fatal("new persistent log not installed")
	}
}
