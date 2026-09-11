package companion_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent/companion"
)

func firstCompanion() companion.Companion {
	limit := 120
	return companion.Companion{
		ParentSessionID: "parent",
		Name:            "reviewer",
		ChildSessionID:  "child",
		Revision:        1,
		SourceRevision:  3,
		Initial: companion.Config{
			Enabled: true,
			Model: agent.Options{
				MaxTokens: &limit,
			},
		},
		Enabled: true,
	}
}

func newRecord(t *testing.T, next companion.Companion) *companion.Record {
	t.Helper()
	plan, err := companion.NewRecord(next, next.Revision-1)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestCompanionRecordFreezesCreationAndReturnsOriginalCommitOnRetry(t *testing.T) {
	history, err := session.NewLog("parent", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	input := firstCompanion()
	plan := newRecord(t, input)
	*input.Initial.Model.MaxTokens = 999
	first, err := history.Commit(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	current, err := companion.CompanionsFromEvents("parent", history.Events())
	if err != nil || len(current) != 1 || *current[0].Initial.Model.MaxTokens != 120 {
		t.Fatalf("creation not detached: %+v, %v", current, err)
	}
	next := current[0].Clone()
	next.Revision++
	next.Enabled = false
	if _, err := history.Commit(t.Context(), newRecord(t, next)); err != nil {
		t.Fatal(err)
	}
	retry, err := history.Commit(t.Context(), plan)
	if err != nil || retry.Barrier != first.Barrier || history.Seq() != 2 {
		t.Fatalf("retry did not reuse original commit: %+v, %v", retry, err)
	}
	input.Initial.Model.MaxTokens = nil
	if _, err := history.Commit(t.Context(), newRecord(t, input)); !errors.Is(err, companion.ErrConflict) {
		t.Fatal("same revision accepted different creation data", err)
	}
	*current[0].Initial.Model.MaxTokens = 1
	readAgain, err := companion.CompanionsFromEvents("parent", history.Events())
	if err != nil || *readAgain[0].Initial.Model.MaxTokens != 120 || readAgain[0].Enabled {
		t.Fatal("returned configuration changed authoritative history", err)
	}
}

func TestCompanionTombstoneRequiresNewChildAndRetainsNameRevision(t *testing.T) {
	history, err := session.NewLog("parent", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	next := firstCompanion()
	if _, err := history.Commit(t.Context(), newRecord(t, next)); err != nil {
		t.Fatal(err)
	}
	next.Revision++
	next.Enabled = false
	next.Removed = true
	if _, err := history.Commit(t.Context(), newRecord(t, next)); err != nil {
		t.Fatal(err)
	}
	// Ordinary recovery appends an end-seed marker; own tombstones must survive it.
	recovered, err := session.NewLog("parent", session.CreateOptions{
		Seed: history.Events(),
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := companion.CompanionsFromEvents("parent", recovered.Events())
	if err != nil || len(current) != 1 || !current[0].Removed || current[0].Enabled {
		t.Fatal("recovery discarded the explicit removal", err)
	}
	next.Revision++
	next.Enabled = true
	next.Removed = false
	if _, err := recovered.Commit(t.Context(), newRecord(t, next)); !errors.Is(err, companion.ErrConflict) {
		t.Fatal("removed child was revived", err)
	}
	next.ChildSessionID = "replacement"
	if _, err := recovered.Commit(t.Context(), newRecord(t, next)); err != nil {
		t.Fatal("explicit replacement rejected", err)
	}
	other := firstCompanion()
	other.Name = "another"
	if _, err := recovered.Commit(t.Context(), newRecord(t, other)); !errors.Is(err, companion.ErrConflict) {
		t.Fatal("historic child reused for another name", err)
	}
	inherited, err := companion.CompanionsFromEvents("descendant", recovered.Events())
	if err != nil || len(inherited) != 0 {
		t.Fatal("ancestor facts became descendant relationships", err)
	}
}

func TestCompanionRecordRejectsInvalidAndConflictingChanges(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*companion.Companion)
	}{
		{
			name:   "no parent",
			change: func(next *companion.Companion) { next.ParentSessionID = "" },
		},
		{
			name:   "self child",
			change: func(next *companion.Companion) { next.ChildSessionID = next.ParentSessionID },
		},
		{
			name:   "unresolved inheritance",
			change: func(next *companion.Companion) { next.Initial.Inherit.Model = true },
		},
		{
			name:   "removed but enabled",
			change: func(next *companion.Companion) { next.Removed = true },
		},
		{
			name:   "unsafe revision",
			change: func(next *companion.Companion) { next.Revision = 1 << 53 },
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			next := firstCompanion()
			scenario.change(&next)
			if _, err := companion.NewRecord(next, 0); err == nil {
				t.Fatal("invalid record accepted")
			}
		})
	}
	for _, scenario := range []struct {
		name   string
		change func(*companion.Companion)
	}{
		{
			name:   "replace live child",
			change: func(next *companion.Companion) { next.ChildSessionID = "other" },
		},
		{
			name:   "rewrite initial config",
			change: func(next *companion.Companion) { *next.Initial.Model.MaxTokens = 999 },
		},
		{
			name:   "rewrite initial source",
			change: func(next *companion.Companion) { next.SourceRevision++ },
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			history, err := session.NewLog("parent", session.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			next := firstCompanion()
			if _, err := history.Commit(t.Context(), newRecord(t, next)); err != nil {
				t.Fatal(err)
			}
			next.Revision++
			scenario.change(&next)
			if _, err := history.Commit(t.Context(), newRecord(t, next)); !errors.Is(err, companion.ErrConflict) {
				t.Fatal("conflicting change accepted", err)
			}
		})
	}
}

func TestCompanionProjectionRejectsCorruptHistoryAndWrongSessionWrite(t *testing.T) {
	first := firstCompanion()
	data, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	entry := session.Event{
		Type: "companion/instance",
		Data: data,
	}
	if _, err := companion.CompanionsFromEvents("parent", []session.Event{entry, entry}); !errors.Is(err, companion.ErrHistory) {
		t.Fatal("duplicate version accepted", err)
	}
	first.Revision = 3
	data, err = json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := companion.CompanionsFromEvents("parent", []session.Event{entry, {
		Type: "companion/instance",
		Data: data,
	}}); !errors.Is(err, companion.ErrHistory) {
		t.Fatal("revision gap accepted", err)
	}
	wrong, err := session.NewLog("other", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrong.Commit(t.Context(), newRecord(t, firstCompanion())); !errors.Is(err, companion.ErrInvalid) {
		t.Fatal("relationship written to a different parent", err)
	}
}
