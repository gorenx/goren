package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	boundcontract "github.com/gorenx/goren/subagent/bound"
	"github.com/gorenx/goren/subagent/internal/bound"
)

func TestAdapterPersistsAndComparesDefinitionRevisions(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	databasePath := filepath.Join(t.TempDir(), "bound-definitions.sqlite")
	storage, err := Open(
		requestContext,
		Config{
			Path:        databasePath,
			JournalMode: JournalWAL,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	first := definitionFixture(t, 1, true)
	if err = storage.Create(requestContext, first); err != nil {
		t.Fatal(err)
	}
	if err = storage.Create(requestContext, first); !errors.Is(
		err,
		bound.ErrDefinitionExists,
	) {
		t.Fatalf("duplicate Create error = %v", err)
	}
	second := definitionFixture(t, 2, false)
	if err = storage.Replace(requestContext, 1, second); err != nil {
		t.Fatal(err)
	}
	third := definitionFixture(t, 2, true)
	if err = storage.Replace(requestContext, 1, third); !errors.Is(
		err,
		bound.ErrDefinitionRevisionConflict,
	) {
		t.Fatalf("stale Replace error = %v", err)
	}
	missing, createErr := boundcontract.NewDefinition(
		boundcontract.Draft{
			Name:         "missing",
			Enabled:      true,
			SystemPrompt: "missing",
		},
		2,
	)
	if createErr != nil {
		t.Fatal(createErr)
	}
	if err = storage.Replace(requestContext, 1, missing); !errors.Is(
		err,
		bound.ErrDefinitionNotFound,
	) {
		t.Fatalf("missing Replace error = %v", err)
	}
	if err = storage.Close(requestContext); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(
		requestContext,
		Config{
			Path: databasePath,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := reopened.Close(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	})
	definitions, err := reopened.Load(requestContext)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 1 ||
		definitions[0].Name != second.Name ||
		definitions[0].Revision != second.Revision ||
		definitions[0].Enabled {
		t.Fatalf("reopened Definitions = %#v", definitions)
	}
}

func TestAdapterRejectsInconsistentStoredDefinition(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	storage, err := Open(
		requestContext,
		Config{
			Path: ":memory:",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := storage.Close(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	})
	_, err = storage.database.ExecContext(
		requestContext,
		`INSERT INTO bound_definitions (name, revision, definition_json) VALUES (?, ?, ?)`,
		"row-name",
		1,
		[]byte(`{"name":"payload-name","revision":1,"enabled":true,"systemPrompt":"prompt","extensions":[]}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = storage.Load(requestContext); err == nil {
		t.Fatal("inconsistent stored Definition was accepted")
	}
}

func definitionFixture(
	testingContext *testing.T,
	revision int64,
	isEnabled bool,
) boundcontract.Definition {
	testingContext.Helper()
	definitionValue, err := boundcontract.NewDefinition(
		boundcontract.Draft{
			Name:         "researcher",
			Enabled:      isEnabled,
			SystemPrompt: "research",
			Extensions:   []string{"report"},
		},
		revision,
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	return definitionValue
}
