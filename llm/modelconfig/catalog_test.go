package modelconfig_test

import (
	"errors"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/modelconfig"
	"testing"
)

func initialDefinition() modelconfig.Definition {
	limit := 128
	return modelconfig.Definition{
		Default: llm.CallConfig{
			Provider:        "first",
			Model:           "one",
			MaxTokens:       &limit,
			Stop:            []string{"end"},
			ReasoningEffort: "high",
		},
		Providers: []modelconfig.Provider{
			{
				ID:            "first",
				CredentialRef: "FIRST_KEY",
			},
			{
				ID:            "second",
				CredentialRef: "SECOND_KEY",
			},
		},
	}
}

func TestCatalogSnapshotsAndUpdatesAreIsolated(t *testing.T) {
	initial := initialDefinition()
	owner, err := modelconfig.NewCatalog(initial)
	if err != nil {
		t.Fatal(err)
	}
	initial.Providers[0].CredentialRef = "CHANGED"
	*initial.Default.MaxTokens = 1
	before := owner.Current()
	proposed := owner.Current().Definition
	proposed.Default.Model = "two"
	next, err := owner.Replace(before.Version, proposed)
	if err != nil {
		t.Fatal(err)
	}
	proposed.Default.Stop[0] = "mutated"
	next.Definition.Default.Stop[0] = "also-mutated"
	if before.Definition.Default.Model != "one" || *before.Definition.Default.MaxTokens != 128 || before.Definition.Providers[0].CredentialRef != "FIRST_KEY" {
		t.Fatal("initial or prior snapshot was mutated")
	}
	if got := owner.Current(); got.Version != 2 || got.Definition.Default.Model != "two" || got.Definition.Default.Stop[0] != "end" {
		t.Fatal("published configuration was not isolated")
	}
	if _, err := owner.Read(1); !errors.Is(err, modelconfig.ErrVersion) {
		t.Fatalf("old version error = %v", err)
	}
}

func TestRejectedUpdatesDoNotPublish(t *testing.T) {
	owner, err := modelconfig.NewCatalog(initialDefinition())
	if err != nil {
		t.Fatal(err)
	}
	proposed := owner.Current().Definition
	proposed.Default.Provider = "missing"
	if _, err := owner.Replace(1, proposed); err == nil {
		t.Fatal("undefined default provider accepted")
	}
	if _, err := owner.Replace(0, initialDefinition()); !errors.Is(err, modelconfig.ErrVersion) {
		t.Fatalf("version conflict = %v", err)
	}
	if owner.Current().Version != 1 {
		t.Fatal("failed update changed version")
	}
}

func TestResolveOverridesAndProvenance(t *testing.T) {
	owner, err := modelconfig.NewCatalog(initialDefinition())
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := owner.Resolve(1, llm.CallConfig{
		Provider: "second",
		Model:    "other",
		Stop:     []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Version != 1 || resolved.CredentialRef != "SECOND_KEY" || resolved.Config.Model != "other" || resolved.Config.ReasoningEffort != "" || *resolved.Config.MaxTokens != 128 || resolved.Config.Stop == nil || len(resolved.Config.Stop) != 0 {
		t.Fatal("overrides or provenance were lost")
	}
	for _, proposed := range []llm.CallConfig{
		{
			Provider: "second",
		},
		{
			Provider: "missing",
			Model:    "other",
		},
	} {
		if _, err := owner.Resolve(1, proposed); err == nil {
			t.Fatal("invalid route accepted")
		}
	}
	if _, err := owner.Resolve(2, llm.CallConfig{}); !errors.Is(err, modelconfig.ErrVersion) {
		t.Fatalf("resolve conflict = %v", err)
	}
}
