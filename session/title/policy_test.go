package title

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

func policyFixture(t *testing.T) (*Policy, []session.Event) {
	t.Helper()
	rules, err := NewPolicy(Config{
		MaxTitleBytes:    80,
		FallbackMaxWords: 3,
		FallbackMaxBytes: 40,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("one two three four")},
		Source:  agentmessage.UserMessageSource{Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return rules, []session.Event{{
		Type: session.UserMessageEventName,
		Seq:  0,
		Data: data,
	}}
}

func titleEntry(t *testing.T, seq int64, text string) session.Event {
	t.Helper()
	data, err := json.Marshal(EventData{
		Title:       text,
		MessageSeqs: []int64{},
		Source:      UserSource{Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return session.Event{
		Type: TitleEventName,
		Seq:  seq,
		Data: data,
	}
}

func TestPolicyFallbackRechecksCommittedTitle(t *testing.T) {
	rules, entries := policyFixture(t)
	plan := rules.Fallback()
	drafts, err := plan.Build(context.Background(), session.Snapshot{Events: entries})
	if err != nil || len(drafts) != 1 {
		t.Fatalf("fallback = %d, %v", len(drafts), err)
	}
	conversation, err := session.NewLog("title", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	committed, err := conversation.Commit(context.Background(), session.Batch(drafts...))
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := Fold(committed.Events)
	if err != nil || accepted.Title != "one two three" {
		t.Fatalf("fallback = %#v, %v", accepted, err)
	}
	entries = append(entries, titleEntry(t, 1, "manual"))
	drafts, err = plan.Build(context.Background(), session.Snapshot{Events: entries})
	if err != nil || len(drafts) != 0 {
		t.Fatal("fallback overwrote title")
	}
}

func TestPolicyRejectsSupersededGeneration(t *testing.T) {
	rules, entries := policyFixture(t)
	result := ProviderResult{
		Title:       "generated",
		MessageSeqs: []int64{0},
	}
	plan, err := rules.Accept(entries, "test", result, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Build(context.Background(), session.Snapshot{Events: entries}); err != nil {
		t.Fatal(err)
	}
	renamed := append(append([]session.Event(nil), entries...), titleEntry(t, 1, "manual"))
	if _, err := plan.Build(context.Background(), session.Snapshot{Events: renamed}); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("rename race = %v", err)
	}
	if _, err := rules.Accept(renamed, "test", result, false); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("pinned title = %v", err)
	}
	refresh, err := rules.Accept(renamed, "test", result, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := refresh.Build(context.Background(), session.Snapshot{Events: renamed}); err != nil {
		t.Fatal(err)
	}
	renamed = append(renamed, titleEntry(t, 2, "new manual"))
	if _, err := refresh.Build(context.Background(), session.Snapshot{Events: renamed}); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("refresh overwrote later rename: %v", err)
	}
	next := entries[0].Clone()
	next.Seq = 2
	newer := append(append([]session.Event(nil), entries...), next)
	if _, err := plan.Build(context.Background(), session.Snapshot{Events: newer}); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("new prompt race = %v", err)
	}
}

func TestPolicyRejectsInvalidProviderProvenance(t *testing.T) {
	rules, entries := policyFixture(t)
	for _, sequences := range [][]int64{nil, {0, 0}, {1}, {-1}} {
		_, err := rules.Accept(entries, "test", ProviderResult{
			Title:       "generated",
			MessageSeqs: sequences,
		}, false)
		if err == nil {
			t.Fatalf("accepted message seqs %v", sequences)
		}
	}
	if _, err := rules.Rename("\t\n"); err == nil {
		t.Fatal("empty rename accepted")
	}
}

func TestRefreshFallbackPreservesCurrentStateAndRejectsLaterRename(t *testing.T) {
	rules, messages := policyFixture(t)
	automatic, err := json.Marshal(EventData{
		Title:       "automatic",
		MessageSeqs: []int64{0},
		Source:      FallbackSource{Kind: "fallback"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		entries []session.Event
		changed bool
	}{
		{
			name:    "absent title",
			entries: messages,
			changed: true,
		},
		{
			name:    "manual title",
			entries: append(append([]session.Event(nil), messages...), titleEntry(t, 1, "manual")),
			changed: true,
		},
		{
			name: "automatic title",
			entries: append(append([]session.Event(nil), messages...), session.Event{
				Type: TitleEventName,
				Seq:  1,
				Data: automatic,
			}),
		},
		{
			name:    "no human messages",
			entries: []session.Event{titleEntry(t, 0, "manual")},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			plan, err := rules.RefreshFallback(testCase.entries)
			if err != nil {
				t.Fatal(err)
			}
			drafts, err := plan.Build(context.Background(), session.Snapshot{Events: testCase.entries})
			if err != nil || (len(drafts) == 1) != testCase.changed {
				t.Fatalf("drafts=%d, error=%v", len(drafts), err)
			}
			newer := append(append([]session.Event(nil), testCase.entries...), titleEntry(t, 9, "newer rename"))
			if _, err := plan.Build(context.Background(), session.Snapshot{Events: newer}); !errors.Is(err, ErrSuperseded) {
				t.Fatalf("late rename overwritten: %v", err)
			}
		})
	}
}
