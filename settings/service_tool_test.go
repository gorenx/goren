package settings

import (
	"errors"
	"testing"

	"github.com/gorenx/goren/tools"
)

func TestServiceToolWholeReplacementResetAndRetry(t *testing.T) {
	baseline := exampleBaseline()
	hidden := false
	baseline.Tools = map[string]tools.Config{
		"read": {
			Visible: &hidden,
		},
	}
	identifier := Group{
		Section: ToolSection,
		Name:    "read",
	}
	storage := &memoryStore{}
	toolsets := &toolRecorder{
		invalid: ErrInvalid,
	}
	owner, err := NewService(baseline, State{}, storage, &promptRecorder{}, &connectionRecorder{}, toolsets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.UpdateTool(t.Context(), "read", 0, tools.Config{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid owner target accepted: %v", err)
	}
	if storage.stored.Revision != 0 || toolsets.calls != 0 {
		t.Fatal("rejected tool target was saved or applied")
	}
	toolsets.invalid = nil
	toolsets.failure = errors.New("fixture application failure")
	view, err := owner.UpdateTool(t.Context(), "read", 0, tools.Config{})
	if !errors.Is(err, ErrApplication) || view.State.Version(identifier) != 1 {
		t.Fatalf("application failure lost saved configuration: %v", err)
	}
	_, visible := view.Target.Tools["read"].Resolve()
	if !visible {
		t.Fatal("whole replacement inherited the local visibility flag")
	}
	toolsets.failure = nil
	view, err = owner.RetryApplication(t.Context(), identifier, 1)
	if err != nil || view.Applications[identifier].AppliedRevision != 1 {
		t.Fatalf("retry failed: %v", err)
	}
	view, err = owner.ResetTool(t.Context(), "read", 1)
	if err != nil || view.State.User.Has(identifier) {
		t.Fatalf("reset did not clear the full override: %v", err)
	}
	_, visible = toolsets.configuration.Resolve()
	if visible || view.State.Version(identifier) != 2 {
		t.Fatal("reset did not restore local visibility and retain revision")
	}
	if _, err := owner.UpdateTool(t.Context(), "read", 0, tools.Config{}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale edit accepted after reset")
	}
}
