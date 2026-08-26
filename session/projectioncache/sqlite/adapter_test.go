package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/session/projectioncache"
)

func TestAdapterCreatesReopensAndReplacesCheckpoint(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "projection-cache.sqlite")
	first, err := Open(context.Background(), Config{
		Path:        databasePath,
		JournalMode: JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	workingDirectory := filepath.Dir(databasePath)
	initial := projectioncache.CheckpointRecord{
		Identity: projectioncache.LogIdentity{
			CreatedAt: 10,
			CWD:       &workingDirectory,
		},
		Rows: sessionprojection.Checkpoint{
			"title": {
				Version: 1,
				Seq:     4,
				Value:   json.RawMessage(`{"title":"first"}`),
			},
		},
	}
	if err := first.Replace(context.Background(), "session-1", initial); err != nil {
		t.Fatal(err)
	}
	replacement := projectioncache.CheckpointRecord{
		Identity: projectioncache.LogIdentity{
			CreatedAt: 11,
		},
		Rows: sessionprojection.Checkpoint{
			"title": {
				Version: 2,
				Seq:     9,
				Value:   json.RawMessage(`null`),
			},
		},
	}
	if err := first.Replace(context.Background(), "session-1", replacement); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := Open(context.Background(), Config{
		Path:        databasePath,
		JournalMode: JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(context.Background())
	loaded, err := second.LoadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	record, found := loaded[session.SessionID("session-1")]
	if !found {
		t.Fatalf("records = %#v", loaded)
	}
	if record.Identity.CreatedAt != 11 || record.Identity.CWD != nil {
		t.Fatalf("identity = %#v", record.Identity)
	}
	row := record.Rows["title"]
	if row.Version != 2 || row.Seq != 9 || string(row.Value) != "null" {
		t.Fatalf("row = %#v", row)
	}
}

func TestAdapterRejectsForeignApplicationIdentity(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "foreign.sqlite")
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("PRAGMA application_id = 1234"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), Config{
		Path:        databasePath,
		JournalMode: JournalWAL,
	}); err == nil {
		t.Fatal("Open accepted a foreign database")
	}
}

func TestAdapterRebuildsKnownOldCheckpointSchema(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "old.sqlite")
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		"PRAGMA application_id = 1196442440",
		"PRAGMA user_version = 99",
		"CREATE TABLE session_projection_checkpoints (session_id TEXT PRIMARY KEY, obsolete TEXT)",
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	checkpointAdapter, err := Open(context.Background(), Config{
		Path:        databasePath,
		JournalMode: JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer checkpointAdapter.Close(context.Background())
	if err := checkpointAdapter.Replace(
		context.Background(),
		"rebuilt",
		projectioncache.CheckpointRecord{
			Identity: projectioncache.LogIdentity{
				CreatedAt: 1,
			},
			Rows: sessionprojection.Checkpoint{},
		},
	); err != nil {
		t.Fatal(err)
	}
}

func TestAdapterDatabaseConstraintRejectsInvalidJSON(t *testing.T) {
	checkpointAdapter, err := Open(context.Background(), Config{
		Path:        ":memory:",
		JournalMode: JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer checkpointAdapter.Close(context.Background())
	_, err = checkpointAdapter.database.ExecContext(
		context.Background(),
		"INSERT INTO session_projection_checkpoints (session_id, created_at, rows_json) VALUES (?, ?, ?)",
		"invalid",
		1,
		[]byte(`{`),
	)
	if err == nil {
		t.Fatal("SQLite accepted invalid rows JSON")
	}
}

func TestAdapterHonorsCancelledContext(t *testing.T) {
	checkpointAdapter, err := Open(context.Background(), Config{
		Path:        ":memory:",
		JournalMode: JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer checkpointAdapter.Close(context.Background())
	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	if _, err := checkpointAdapter.LoadAll(requestContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("LoadAll error = %v, want context.Canceled", err)
	}
	if err := checkpointAdapter.Replace(
		requestContext,
		"cancelled",
		projectioncache.CheckpointRecord{
			Identity: projectioncache.LogIdentity{
				CreatedAt: 1,
			},
			Rows: sessionprojection.Checkpoint{},
		},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("Replace error = %v, want context.Canceled", err)
	}
}
