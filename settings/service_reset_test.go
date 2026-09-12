package settings

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestServiceNamedGroupsSeparateTypesAndNames(t *testing.T) {
	baseline := exampleBaseline()
	baseline.Providers["shared"] = ProviderConfig{}
	baseline.Prompts["shared"] = PromptConfig{}
	storage := &memoryStore{}
	owner, err := NewService(baseline, State{}, storage, &connectionRecorder{}, &toolRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	promptIdentity := Group{
		Section: PromptSection,
		Name:    "shared",
	}
	providerIdentity := Group{
		Section: ProviderSection,
		Name:    "shared",
	}
	ref := "NAMED_API_KEY"
	view, err := owner.UpdateProvider(t.Context(), "shared", 0, ProviderConfig{
		APIKeyEnv: &ref,
	})
	if err != nil || view.State.Version(providerIdentity) != 1 || !view.PendingRestart["shared"] {
		t.Fatalf("provider update: %v", err)
	}
	view, err = owner.UpdatePrompt(t.Context(), "shared", 0, PromptConfig{})
	if err != nil || view.State.Version(providerIdentity) != 1 {
		t.Fatalf("same name in other type collided: %v", err)
	}
	if view.State.Version(promptGroup) != 0 || view.State.Version(providerGroup) != 0 {
		t.Fatal("unrelated names changed version")
	}
	view, err = owner.ResetProvider(t.Context(), "shared", 1)
	if err != nil ||
		view.PendingRestart["shared"] ||
		view.State.User.Has(providerIdentity) ||
		!view.State.User.Has(promptIdentity) {
		t.Fatalf("reset crossed group identity: %v", err)
	}
	if _, err := owner.UpdateProvider(t.Context(), "uninstalled", 0, ProviderConfig{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown Provider accepted: %v", err)
	}
	if _, err := owner.UpdatePrompt(t.Context(), "uninstalled", 0, PromptConfig{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown Prompt accepted: %v", err)
	}
	if _, err := owner.ResetPrompt(t.Context(), "uninstalled", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown reset accepted: %v", err)
	}
}

func TestServiceResetFailureAndCancellation(t *testing.T) {
	storage := &memoryStore{}
	owner, err := NewService(exampleBaseline(), State{}, storage, &connectionRecorder{}, &toolRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := owner.UpdatePrompt(t.Context(), "default", 0, PromptConfig{})
	if err != nil {
		t.Fatal(err)
	}
	storage.failure = ErrStorage
	if _, err := owner.ResetPrompt(t.Context(), "default", 1); !errors.Is(err, ErrStorage) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved, owner.Snapshot()) {
		t.Fatal("failed reset changed state")
	}
	storage.failure = nil
	view, err := owner.ResetPrompt(t.Context(), "default", 1)
	if err != nil ||
		view.State.Revision != 2 {
		t.Fatal("reset did not persist its revision")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := owner.UpdateProvider(canceled, "deepseek", 0, ProviderConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled edit was accepted")
	}
}
