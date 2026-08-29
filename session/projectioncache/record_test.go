package projectioncache

import (
	"encoding/json"
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
