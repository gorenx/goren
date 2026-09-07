package projectioncache

import (
	"encoding/json"
	"github.com/gorenx/goren/session"
	"testing"

	sessproj "github.com/gorenx/goren/session/projection"
)

func TestValidateCheckpointRecordDetachesMutableInput(t *testing.T) {
	workingDirectory := "/original"
	value := json.RawMessage(`{"title":"original"}`)
	record, err := ValidateCheckpointRecord(
		"detached",
		CheckpointRecord{
			Identity: LogIdentity{
				CreatedAt: 1,
				CWD:       &workingDirectory,
			},
			Rows: sessproj.Checkpoint{
				"title": {
					Version: 1,
					Seq:     2,
					Value:   value,
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	workingDirectory = "/mutated"
	value[10] = 'm'
	if record.Identity.CWD == nil || *record.Identity.CWD != "/original" ||
		string(record.Rows["title"].Value) != `{"title":"original"}` {
		t.Fatalf("detached record = %#v", record)
	}
}

func TestMergeKeepsNewerCompatibleRowsWhileAdvancingOtherUnits(t *testing.T) {
	current := CheckpointRecord{
		Identity: LogIdentity{
			CreatedAt: 1,
		},
		Rows: sessproj.Checkpoint{
			"ahead": {
				Version: 1,
				Seq:     10,
				Value:   json.RawMessage(`"current"`),
			},
			"behind": {
				Version: 1,
				Seq:     2,
				Value:   json.RawMessage(`"old"`),
			},
			"inactive": {
				Version: 3,
				Seq:     1,
				Value:   json.RawMessage(`true`),
			},
		},
	}
	incoming := CheckpointRecord{
		Identity: current.Identity,
		Rows: sessproj.Checkpoint{
			"ahead": {
				Version: 1,
				Seq:     5,
				Value:   json.RawMessage(`"stale"`),
			},
			"behind": {
				Version: 1,
				Seq:     5,
				Value:   json.RawMessage(`"new"`),
			},
		},
	}
	merged, changed := current.Merge(incoming)
	if !changed || merged.Rows["ahead"].Seq != 10 || merged.Rows["behind"].Seq != 5 || merged.Rows["inactive"].Seq != 1 {
		t.Fatalf("merge = %#v, changed %v", merged, changed)
	}
	incoming.Rows["behind"].Value[1] = '!'
	current.Rows["ahead"].Value[1] = '!'
	if string(merged.Rows["behind"].Value) != `"new"` || string(merged.Rows["ahead"].Value) != `"current"` {
		t.Fatal("merged rows alias inputs")
	}
	if _, changed := merged.Merge(merged); changed {
		t.Fatal("identical record requires replacement")
	}
	replacement := CheckpointRecord{
		Identity: LogIdentity{
			CreatedAt: 2,
		},
		Rows: sessproj.Checkpoint{},
	}
	reset, changed := merged.Merge(replacement)
	if !changed || len(reset.Rows) != 0 {
		t.Fatal("new log inherited old Unit rows")
	}
}

func TestNewCheckpointRequiresMatchingDurableCut(t *testing.T) {
	metadata := session.Header{
		ID:        "conversation",
		CreatedAt: 1,
	}
	rows := sessproj.Checkpoint{
		"first": {
			Version: 1,
			Seq:     2,
			Value:   json.RawMessage(`true`),
		},
	}
	for _, scenario := range []struct {
		name    string
		barrier session.WriteBarrier
		valid   bool
	}{
		{
			name: "matching",
			barrier: session.WriteBarrier{
				SessionID: "conversation",
				NextSeq:   3,
			},
			valid: true,
		},
		{
			name: "not yet durable",
			barrier: session.WriteBarrier{
				SessionID: "conversation",
				NextSeq:   2,
			},
		},
		{
			name: "another session",
			barrier: session.WriteBarrier{
				SessionID: "other",
				NextSeq:   3,
			},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			_, err := NewCheckpoint(metadata, rows, scenario.barrier)
			if (err == nil) != scenario.valid {
				t.Fatalf("valid %v, error %v", scenario.valid, err)
			}
		})
	}
	rows["second"] = sessproj.CheckpointRow{
		Version: 1,
		Seq:     1,
		Value:   json.RawMessage(`true`),
	}
	if _, err := NewCheckpoint(metadata, rows, session.WriteBarrier{
		SessionID: "conversation",
		NextSeq:   3,
	}); err == nil {
		t.Fatal("mixed live cuts accepted")
	}
}

func TestCheckpointSnapshotUsesCompatibleRowsAndDetachesViews(t *testing.T) {
	record := CheckpointRecord{
		Rows: sessproj.Checkpoint{
			"first": {
				Version: 1,
				Seq:     9,
				Value:   json.RawMessage(`0`),
			},
			"second": {
				Version: 1,
				Seq:     4,
				Value:   json.RawMessage(`0`),
			},
		},
	}
	values := sessproj.Values{
		"first":  json.RawMessage(`"one"`),
		"second": json.RawMessage(`"two"`),
	}
	snapshot, err := record.Snapshot(values)
	if err != nil || snapshot == nil || snapshot.AsOfSeq != 4 {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
	values["first"][1] = '!'
	if string(snapshot.Values["first"]) != `"one"` {
		t.Fatal("snapshot aliases supplied views")
	}
	if empty, err := record.Snapshot(nil); err != nil || empty != nil {
		t.Fatal("no compatible Unit values should be a miss")
	}
	if _, err := record.Snapshot(sessproj.Values{
		"missing": json.RawMessage(`null`),
	}); err == nil {
		t.Fatal("view without checkpoint accepted")
	}
}
