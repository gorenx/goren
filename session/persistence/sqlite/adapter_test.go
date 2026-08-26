package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/gorenx/goren/session"
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
	if err := storage.AppendBatch(requestContext, metadata, entries, false); err != nil {
		t.Fatal(err)
	}
	stored, found, err := storage.LoadStored(requestContext, metadata.ID)
	if err != nil || !found {
		t.Fatalf("LoadStored = (%#v, %t, %v)", stored, found, err)
	}
	if stored.Header.ID != metadata.ID || len(stored.Events) != 2 || stored.Events[1].Type != session.TurnEndEventName {
		t.Fatalf("stored prefix = %#v", stored)
	}
	before := stored.Token
	badBatch := []session.Event{
		{Type: "extension/probe", Seq: 2, Time: 3, Data: []byte(`{}`), Ignorable: true},
		{Type: "extension/duplicate", Seq: 0, Time: 4, Data: []byte(`{}`), Ignorable: true},
	}
	if err := storage.AppendBatch(requestContext, metadata, badBatch, true); err == nil {
		t.Fatal("batch containing a duplicate seq committed")
	}
	afterFailure, found, err := storage.LoadStored(requestContext, metadata.ID)
	if err != nil || !found {
		t.Fatalf("LoadStored after rollback = (%#v, %t, %v)", afterFailure, found, err)
	}
	if len(afterFailure.Events) != 2 || afterFailure.Token != before {
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
	if err := storage.AppendBatch(requestContext, metadata, entries, false); err != nil {
		t.Fatal(err)
	}
	_, err = storage.database.ExecContext(requestContext, `
		INSERT INTO events (session_id, seq, type, time, data, source_event_seqs, surface_op, ignorable)
		VALUES (?, 2, 'extension/torn', 3, '{', NULL, NULL, 1)`, string(metadata.ID))
	if err != nil {
		t.Fatal(err)
	}
	stored, found, err := storage.LoadStored(requestContext, metadata.ID)
	if err != nil || !found || len(stored.Events) != 2 || stored.Marker == nil {
		t.Fatalf("torn LoadStored = (%#v, %t, %v)", stored, found, err)
	}
	if err := storage.CommitRepair(requestContext, metadata, stored.Marker, nil); err != nil {
		t.Fatal(err)
	}
	repaired, found, err := storage.LoadStored(requestContext, metadata.ID)
	if err != nil || !found || repaired.Marker != nil || len(repaired.Events) != 2 {
		t.Fatalf("repaired LoadStored = (%#v, %t, %v)", repaired, found, err)
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
	if err := storage.AppendBatch(requestContext, metadata, entries, false); err != nil {
		t.Fatal(err)
	}

	tail, found, err := storage.LoadStoredEventsBefore(
		requestContext,
		metadata.ID,
		nil,
		3,
	)
	if err != nil || !found {
		t.Fatalf("tail window = (%#v, %t, %v)", tail, found, err)
	}
	if !tail.HasEarlier || eventSequences(tail.Events) != "9,8,7" {
		t.Fatalf("tail window = %#v", tail)
	}

	before := int64(7)
	older, found, err := storage.LoadStoredEventsBefore(
		requestContext,
		metadata.ID,
		&before,
		3,
	)
	if err != nil || !found {
		t.Fatalf("older window = (%#v, %t, %v)", older, found, err)
	}
	if !older.HasEarlier || eventSequences(older.Events) != "6,5,4" {
		t.Fatalf("older window = %#v", older)
	}

	before = 2
	head, found, err := storage.LoadStoredEventsBefore(
		requestContext,
		metadata.ID,
		&before,
		5,
	)
	if err != nil || !found {
		t.Fatalf("head window = (%#v, %t, %v)", head, found, err)
	}
	if head.HasEarlier || eventSequences(head.Events) != "1,0" {
		t.Fatalf("head window = %#v", head)
	}
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
