package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
)

func TestAdapterMaterializesHeaderAndBatchAtomically(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	storage, err := Open(requestContext, Config{Path: t.TempDir() + "/sessions.sqlite", JournalMode: JournalWAL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close(context.Background()) })
	metadata, entries := closedTurnLog(t, "atomic-session")
	if err := storage.AppendBatch(requestContext, sesspersist.EventBatch{
		Header:      metadata,
		Events:      entries,
		Materialize: true,
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := storage.Load(requestContext, metadata.ID)
	if err != nil || stored == nil {
		t.Fatalf("LoadStored = (%#v, %v)", stored, err)
	}
	if stored.Header.ID != metadata.ID || len(stored.Events) != 2 || stored.Events[1].Type != session.TurnEndEventName {
		t.Fatalf("stored prefix = %#v", stored)
	}
	before := stored.Revision
	badBatch := []session.Event{
		{Type: "extension/probe", Seq: 2, Time: 3, Data: []byte(`{}`), Ignorable: true},
		{Type: "extension/duplicate", Seq: 0, Time: 4, Data: []byte(`{}`), Ignorable: true},
	}
	if err := storage.AppendBatch(requestContext, sesspersist.EventBatch{
		Header: metadata,
		Events: badBatch,
	}); err == nil {
		t.Fatal("batch containing a duplicate seq committed")
	}
	afterFailure, err := storage.Load(requestContext, metadata.ID)
	if err != nil || afterFailure == nil {
		t.Fatalf("LoadStored after rollback = (%#v, %v)", afterFailure, err)
	}
	if len(afterFailure.Events) != 2 || afterFailure.Revision != before {
		t.Fatalf("failed transaction changed durable state: %#v", afterFailure)
	}
}

func TestAdapterMarksAndRepairsOnlyATornTail(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	storage, err := Open(requestContext, Config{Path: t.TempDir() + "/sessions.sqlite", JournalMode: JournalWAL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close(context.Background()) })
	metadata, entries := closedTurnLog(t, "repair-session")
	if err := storage.AppendBatch(requestContext, sesspersist.EventBatch{
		Header:      metadata,
		Events:      entries,
		Materialize: true,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = storage.database.ExecContext(requestContext, `
		INSERT INTO events (session_id, seq, type, time, data, source_event_seqs, surface_op, ignorable)
		VALUES (?, 2, 'extension/torn', 3, '{', NULL, NULL, 1)`, string(metadata.ID))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := storage.Load(requestContext, metadata.ID)
	if err != nil || stored == nil || len(stored.Events) != 2 || stored.Marker == nil {
		t.Fatalf("torn LoadStored = (%#v, %v)", stored, err)
	}
	if err := storage.CommitRepair(requestContext, sesspersist.LogRepair{
		Header: metadata,
		Marker: stored.Marker,
	}); err != nil {
		t.Fatal(err)
	}
	repaired, err := storage.Load(requestContext, metadata.ID)
	if err != nil || repaired == nil || repaired.Marker != nil || len(repaired.Events) != 2 {
		t.Fatalf("repaired LoadStored = (%#v, %v)", repaired, err)
	}
}

func TestAdapterReadsBoundedEventWindowsBackward(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	storage, err := Open(
		requestContext,
		Config{
			Path:        t.TempDir() + "/sessions.sqlite",
			JournalMode: JournalWAL,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close(context.Background()) })
	metadata := session.Header{
		Version:   session.FormatVersion,
		ID:        "reverse-window",
		CreatedAt: 1,
	}
	entries := make([]session.Event, 10)
	for sequence := range entries {
		entries[sequence] = session.Event{
			Type:      "extension/window",
			Seq:       int64(sequence),
			Time:      int64(sequence + 1),
			Data:      []byte(`{}`),
			Ignorable: true,
		}
	}
	if err := storage.AppendBatch(requestContext, sesspersist.EventBatch{
		Header:      metadata,
		Events:      entries,
		Materialize: true,
	}); err != nil {
		t.Fatal(err)
	}

	tail, err := storage.ReadEventsBefore(
		requestContext,
		metadata.ID,
		sesspersist.EventPage{
			Limit: 3,
		},
	)
	if err != nil || tail == nil {
		t.Fatalf("tail window = (%#v, %v)", tail, err)
	}
	if !tail.HasEarlier || eventSequences(tail.Events) != "9,8,7" {
		t.Fatalf("tail window = %#v", tail)
	}

	before := int64(7)
	older, err := storage.ReadEventsBefore(
		requestContext,
		metadata.ID,
		sesspersist.EventPage{
			BeforeSeq: &before,
			Limit:     3,
		},
	)
	if err != nil || older == nil {
		t.Fatalf("older window = (%#v, %v)", older, err)
	}
	if !older.HasEarlier || eventSequences(older.Events) != "6,5,4" {
		t.Fatalf("older window = %#v", older)
	}

	before = 2
	head, err := storage.ReadEventsBefore(
		requestContext,
		metadata.ID,
		sesspersist.EventPage{
			BeforeSeq: &before,
			Limit:     5,
		},
	)
	if err != nil || head == nil {
		t.Fatalf("head window = (%#v, %v)", head, err)
	}
	if head.HasEarlier || eventSequences(head.Events) != "1,0" {
		t.Fatalf("head window = %#v", head)
	}
}

func TestAdapterReadsBoundedEventSegmentsForward(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	storage, err := Open(
		requestContext,
		Config{
			Path:        t.TempDir() + "/sessions.sqlite",
			JournalMode: JournalWAL,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close(context.Background()) })
	metadata := session.Header{
		Version:   session.FormatVersion,
		ID:        "forward-page",
		CreatedAt: 1,
	}
	entries := make([]session.Event, 6)
	for sequence := range entries {
		entries[sequence] = session.Event{
			Type:      "extension/page",
			Seq:       int64(sequence),
			Time:      int64(sequence + 1),
			Data:      []byte(`{}`),
			Ignorable: true,
		}
	}
	if err := storage.AppendBatch(requestContext, sesspersist.EventBatch{
		Header:      metadata,
		Events:      entries,
		Materialize: true,
	}); err != nil {
		t.Fatal(err)
	}
	first, err := storage.ReadEventsFrom(
		requestContext,
		metadata.ID,
		sesspersist.EventContinuation{
			FromSeq: 0,
			Limit:   2,
		},
	)
	if err != nil || first == nil {
		t.Fatalf("first page = (%#v, %v)", first, err)
	}
	second, err := storage.ReadEventsFrom(
		requestContext,
		metadata.ID,
		sesspersist.EventContinuation{
			FromSeq: 2,
			Limit:   4,
		},
	)
	if err != nil || second == nil {
		t.Fatalf("second page = (%#v, %v)", second, err)
	}
	if !first.HasMore || eventSequences(first.Events) != "0,1" ||
		second.HasMore || eventSequences(second.Events) != "2,3,4,5" ||
		first.Revision != second.Revision {
		t.Fatalf("pages = (%#v, %#v)", first, second)
	}
}

func TestAdapterListsSessionsWithStableCursor(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	storage, err := Open(
		requestContext,
		Config{
			Path:        t.TempDir() + "/sessions.sqlite",
			JournalMode: JournalWAL,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close(context.Background()) })
	materialize := func(identifier session.SessionID, createdAt int64) {
		t.Helper()
		if appendErr := storage.AppendBatch(requestContext, sesspersist.EventBatch{
			Header: session.Header{
				Version:   session.FormatVersion,
				ID:        identifier,
				CreatedAt: createdAt,
			},
			Materialize: true,
		}); appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	materialize("session-30", 30)
	materialize("session-20-a", 20)
	materialize("session-20-b", 20)
	materialize("session-10", 10)

	first, err := storage.List(requestContext, sesspersist.SessionPage{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if sessionIDs(first.Snapshots) != "session-30,session-20-a" || first.NextCursor == nil {
		t.Fatalf("first page = %#v", first)
	}

	materialize("session-40", 40)
	second, err := storage.List(
		requestContext,
		sesspersist.SessionPage{
			Cursor: first.NextCursor,
			Limit:  2,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if sessionIDs(second.Snapshots) != "session-20-b,session-10" || second.NextCursor != nil {
		t.Fatalf("second page = %#v", second)
	}
}

func sessionIDs(snapshots []sesspersist.Snapshot) string {
	identifiers := make([]string, len(snapshots))
	for index, storedSnapshot := range snapshots {
		identifiers[index] = string(storedSnapshot.Header.ID)
	}
	return strings.Join(identifiers, ",")
}

func eventSequences(entries []session.Event) string {
	sequences := make([]string, len(entries))
	for index, entry := range entries {
		sequences[index] = fmt.Sprintf("%d", entry.Seq)
	}
	return strings.Join(sequences, ",")
}

func TestOpenRefusesForeignUnversionedDatabase(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	path := t.TempDir() + "/foreign.sqlite"
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(requestContext, "CREATE TABLE foreign_table (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if storage, err := Open(requestContext, Config{Path: path, JournalMode: JournalWAL}); err == nil {
		_ = storage.Close(context.Background())
		t.Fatal("foreign database was accepted")
	}
}

func closedTurnLog(t *testing.T, identifier session.SessionID) (session.Header, []session.Event) {
	t.Helper()
	conversation, err := session.New(identifier, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	{
		draft, err := session.NewEventDraft(session.TurnStarted, session.TurnStart{Turn: 1})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	{
		draft, err := session.NewEventDraft(session.TurnEnded, session.TurnEnd{
			Turn: 1, Reason: session.TurnCompleted{},
		})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return conversation.Header(), conversation.Events()
}
