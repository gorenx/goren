package settings

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/mcpclient"
)

type memoryStore struct {
	stored    State
	failure   error
	uncertain bool
}

func (memory *memoryStore) Load(context.Context) (State, error) {
	return memory.stored.Clone(), nil
}

func (memory *memoryStore) Commit(_ context.Context, change ChangeSet) error {
	if memory.failure != nil {
		return memory.failure
	}
	if memory.stored.Revision != change.ExpectedRevision {
		return ErrConflict
	}
	memory.stored = change.Next.Clone()
	if memory.uncertain {
		return ErrUncertain
	}
	return nil
}

type connectionRecorder struct {
	invalid error
}

func (recorder *connectionRecorder) ValidateMCP(context.Context, []mcpclient.Config) error {
	return recorder.invalid
}

var providerGroup = Group{
	Section: ProviderSection,
	Name:    "deepseek",
}

var promptGroup = Group{
	Section: PromptSection,
	Name:    "default",
}

func mcpGroup(name string) Group {
	return Group{
		Section: MCPSection,
		Name:    name,
	}
}

func exampleBaseline() Directory {
	persona := "local persona"
	endpoint := "https://example.test"
	disabled := false
	return Directory{
		Values: Values{
			Providers: map[string]ProviderConfig{
				"deepseek": {
					BaseURL: &endpoint,
				},
			},
			Prompts: map[string]PromptConfig{
				"default": {
					Persona:                &persona,
					IncludeHarnessIdentity: &disabled,
				},
			},
			MCP: map[string]MCPConfig{},
		},
	}
}

func TestServiceReplacesCompletePromptAndSeparatesEmptyFromReset(t *testing.T) {
	storage := &memoryStore{}
	owner, err := NewService(exampleBaseline(), State{}, storage, &connectionRecorder{}, &toolRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	persona := "custom"
	view, err := owner.UpdatePrompt(t.Context(), "default", 0, PromptConfig{
		Persona: &persona,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Target.Prompts["default"].IncludeHarnessIdentity != nil {
		t.Fatal("full replacement inherited an omitted local field")
	}
	if view.State.Version(promptGroup) != 1 {
		t.Fatal("Prompt revision was not saved")
	}
	*view.State.User.Prompts["default"].Persona = "caller mutation"
	if *owner.Snapshot().State.User.Prompts["default"].Persona != "custom" {
		t.Fatal("snapshot aliases owner state")
	}
	view, err = owner.UpdatePrompt(t.Context(), "default", 1, PromptConfig{})
	if err != nil || view.Target.Prompts["default"].Persona != nil {
		t.Fatalf("empty configuration was treated as a patch: %v", err)
	}
	if !view.State.User.Has(promptGroup) || view.State.Version(promptGroup) != 2 {
		t.Fatal("empty configuration did not create an explicit override")
	}
	view, err = owner.UpdatePrompt(t.Context(), "default", 2, PromptConfig{})
	if err != nil ||
		view.State.Version(promptGroup) != 2 {
		t.Fatal("identical complete replacement was not a no-op")
	}
	view, err = owner.ResetPrompt(t.Context(), "default", 2)
	if err != nil || *view.Target.Prompts["default"].Persona != "local persona" {
		t.Fatalf("reset did not restore complete local group: %v", err)
	}
	if view.State.User.Has(promptGroup) || view.State.Version(promptGroup) != 3 {
		t.Fatal("reset lost revision or retained override")
	}
	if _, err := owner.UpdatePrompt(t.Context(), "default", 0, PromptConfig{}); !errors.Is(err, ErrConflict) {
		t.Fatal("reset admitted stale editor")
	}
}

func TestServicePublishesSavedValuesOnlyAfterPersistence(t *testing.T) {
	storage := &memoryStore{
		failure: ErrStorage,
	}
	owner, err := NewService(exampleBaseline(), State{}, storage, &connectionRecorder{}, &toolRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	persona := "saved"
	configuration := PromptConfig{
		Persona: &persona,
	}
	if _, err := owner.UpdatePrompt(t.Context(), "default", 0, configuration); !errors.Is(err, ErrStorage) {
		t.Fatal(err)
	}
	if owner.Snapshot().State.Revision != 0 {
		t.Fatal("storage failure changed the saved snapshot")
	}
	storage.failure = nil
	view, err := owner.UpdatePrompt(t.Context(), "default", 0, configuration)
	if err != nil || view.State.Revision != 1 {
		t.Fatalf("save did not remain independent of runtime application: %v", err)
	}
}

func TestServiceReconcilesUncertainCommitBeforeReturning(t *testing.T) {
	storage := &memoryStore{
		uncertain: true,
	}
	owner, err := NewService(exampleBaseline(), State{}, storage, &connectionRecorder{}, &toolRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	view, err := owner.UpdatePrompt(t.Context(), "default", 0, PromptConfig{})
	if err != nil ||
		view.State.Revision != 1 {
		t.Fatalf("uncertain commit was not verified: %v", err)
	}
}

func TestResolveDoesNotInheritNestedProviderFields(t *testing.T) {
	baseline := exampleBaseline()
	initial := 1000.0
	maximum := 5000.0
	baseline.Providers["deepseek"] = ProviderConfig{
		RetryPolicy: &RetryConfig{
			Backoff: &llm.BackoffConfig{
				InitialDelayMS: &initial,
				MaxDelayMS:     &maximum,
			},
		},
	}
	nextDelay := 2000.0
	overrides := Directory{
		Values: Values{
			Providers: map[string]ProviderConfig{
				"deepseek": {
					RetryPolicy: &RetryConfig{
						Backoff: &llm.BackoffConfig{
							InitialDelayMS: &nextDelay,
						},
					},
				},
			},
		},
	}
	resolved, err := Resolve(baseline, overrides)
	if err != nil {
		t.Fatal(err)
	}
	policy := resolved.Providers["deepseek"].RetryPolicy
	if policy.Backoff.MaxDelayMS != nil || *policy.Backoff.InitialDelayMS != 2000 {
		t.Fatal("nested fields inherited local policy")
	}
	*policy.Backoff.InitialDelayMS = 3000
	if nextDelay != 2000 {
		t.Fatal("resolved policy aliases input")
	}
}

func TestServiceRejectsMCPNameConflictBeforePersistence(t *testing.T) {
	storage := &memoryStore{}
	connections := &connectionRecorder{
		invalid: errors.New("name belongs to stdio"),
	}
	owner, err := NewService(exampleBaseline(), State{}, storage, connections, &toolRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.UpdateMCP(t.Context(), "local", 0, namedMCP("local", "https://example.test"))
	if err == nil ||
		storage.stored.Revision != 0 {
		t.Fatal("invalid MCP target saved or applied")
	}
}

func TestConfigurationCodecRejectsNoncanonicalAndSecretInputs(t *testing.T) {
	cases := []string{
		`{"APIKeyEnv":"REF"}`,
		`{"apiKey":"do-not-echo"}`,
		`{"apiKeyEnv":null}`,
		`{"apiKeyEnv":"A","apiKeyEnv":"B"}`,
		`{"retryPolicy":{"backoff":{"InitialDelayMs":1}}}`,
		`{"retryPolicy":{"mode":null}}`,
		`{"streamIdleTimeoutMs":"1000"}`,
	}
	for _, encoded := range cases {
		if _, err := DecodeProvider([]byte(encoded)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("input %s accepted or unsafe error: %v", encoded, err)
		}
	}
	decoded, err := DecodePrompt([]byte(`{"persona":"","includeRuntimeContext":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Persona == nil ||
		decoded.IncludeRuntimeContext == nil ||
		*decoded.IncludeRuntimeContext {
		t.Fatal("codec lost presence")
	}
	copied := decoded.Clone()
	if !reflect.DeepEqual(copied, decoded) {
		t.Fatal("clone changed values")
	}
}

type toolRecorder struct {
	invalid error
}

func (recorder *toolRecorder) ValidateTool(context.Context, string) error { return recorder.invalid }
