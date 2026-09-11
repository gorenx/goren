package settings

import (
	"errors"
	"testing"

	"github.com/gorenx/goren/subagent/companion"
	"github.com/gorenx/goren/systemprompt"
)

func TestCompanionDirectoryReplacementResetAndIsolation(t *testing.T) {
	baseline := exampleBaseline()
	baseline.Companions = map[string]companion.Config{
		"reviewer": {
			Enabled: true,
			Prompt: systemprompt.Config{
				Persona: "local",
			},
		},
	}
	storage := &memoryStore{}
	prompts := &promptRecorder{}
	connections := &connectionRecorder{}
	toolsets := &toolRecorder{}
	owner, err := NewService(baseline, State{}, storage, prompts, connections, toolsets)
	if err != nil {
		t.Fatal(err)
	}
	identifier := Group{
		Section: CompanionSection,
		Name:    "reviewer",
	}
	view, err := owner.UpdateCompanion(t.Context(), "reviewer", 0, companion.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if view.Target.Companions["reviewer"].Enabled || view.Target.Companions["reviewer"].Prompt.Persona != "" {
		t.Fatal("replacement inherited omitted local values")
	}
	if view.Applications[identifier].AppliedRevision != 1 {
		t.Fatal("directory revision not adopted")
	}
	view, err = owner.ResetCompanion(t.Context(), "reviewer", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Target.Companions["reviewer"].Enabled || view.State.Version(identifier) != 2 || view.State.User.Has(identifier) {
		t.Fatal("reset lost baseline or tombstone")
	}
	if _, err := owner.UpdateCompanion(t.Context(), "reviewer", 1, companion.Config{}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale edit accepted after reset")
	}
	if _, err := owner.UpdateCompanion(t.Context(), "new", 0, companion.Config{
		Inherit: companion.Inheritance{
			Tools: "some",
		},
	}); !errors.Is(err, ErrInvalid) {
		t.Fatal("partial tool inheritance accepted")
	}
	if _, err := owner.RetryApplication(t.Context(), identifier, 2); err != nil {
		t.Fatal(err)
	}
	if prompts.calls != 0 || connections.calls != 0 || toolsets.calls != 0 {
		t.Fatal("directory mutation applied configuration to running owners")
	}
	if storage.stored.Revision != 2 {
		t.Fatal("rejected edit or retry wrote a revision")
	}
}
