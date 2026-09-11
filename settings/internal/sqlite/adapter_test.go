package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gorenx/goren/mcpclient"
	"github.com/gorenx/goren/settings"
	"github.com/gorenx/goren/tools"
)

func TestAdapterPersistsSparseOverridesWithConditionalRevision(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "settings.sqlite")
	storage, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	empty := ""
	falseValue := false
	next := settings.State{
		Revision: 1,
		Versions: map[settings.Group]int64{
			storedGroup(settings.PromptSection, "default"): 1,
		},
		User: settings.Values{
			Prompts: map[string]settings.PromptConfig{
				"default": {
					Persona:               &empty,
					IncludeRuntimeContext: &falseValue,
				},
			},
		},
	}
	change := settings.ChangeSet{
		ExpectedRevision: 0,
		Next:             next,
	}
	if err := storage.Commit(ctx, change); err != nil {
		t.Fatal(err)
	}
	if err := storage.Commit(ctx, change); !errors.Is(err, settings.ErrConflict) {
		t.Fatalf("stale commit = %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	retained, err := Inspect(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(retained, next) {
		t.Fatalf("persisted state changed: %#v", retained)
	}
}

func TestInspectHasNoFilesystemEffects(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	path := filepath.Join(directory, "absent", "settings.sqlite")
	if _, err := Inspect(ctx, path); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("inspection created files: %v %v", entries, err)
	}
	path = filepath.Join(directory, "settings.sqlite")
	storage, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(ctx, path); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadDir(directory)
	if err != nil || len(after) != len(before) {
		t.Fatal("read-only inspection created auxiliary files")
	}
}

func TestCommitFailureRollsBackRevisionAndOtherGroups(t *testing.T) {
	ctx := context.Background()
	storage, err := Open(ctx, filepath.Join(t.TempDir(), "settings.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	_, err = storage.database.ExecContext(ctx, `CREATE TRIGGER reject_prompt BEFORE INSERT ON settings_sections WHEN NEW.namespace = 'prompt/default' BEGIN SELECT RAISE(ABORT, 'test failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Commit(ctx, settings.ChangeSet{
		ExpectedRevision: 0,
		Next: settings.State{
			Revision: 1,
			Versions: map[settings.Group]int64{
				storedGroup(settings.PromptSection, "default"): 1,
			},
		},
	}); err == nil {
		t.Fatal("failed transaction returned success")
	}
	stored, err := storage.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, settings.State{}) {
		t.Fatal("failed transaction left partial settings")
	}
}

func TestAdapterPersistsNamedMCPGroupsAndClearedRevisions(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "settings.sqlite")
	storage, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	initial := settings.State{
		Revision: 2,
		Versions: map[settings.Group]int64{
			storedGroup(settings.MCPSection, "testa"):      1,
			storedGroup(settings.MCPSection, "other/name"): 2,
		},
		User: settings.Values{
			MCP: map[string]settings.MCPConfig{
				"testa": {
					Enabled: true,
					Connection: mcpclient.Config{
						ServerName: "testa",
						Transport:  "streamable-http",
						URL:        "https://a.example.test",
					},
				},
				"other/name": {
					Enabled: false,
					Connection: mcpclient.Config{
						ServerName: "other/name",
						Transport:  "streamable-http",
						URL:        "https://b.example.test",
					},
				},
			},
		},
	}
	if err := storage.Commit(ctx, settings.ChangeSet{
		ExpectedRevision: 0,
		Next:             initial,
	}); err != nil {
		t.Fatal(err)
	}
	next := initial.Clone()
	next.Revision = 3
	next.Versions[storedGroup(settings.MCPSection, "testa")] = 3
	delete(next.User.MCP, "testa")
	if err := storage.Commit(ctx, settings.ChangeSet{
		ExpectedRevision: 2,
		Next:             next,
	}); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := Inspect(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, next) {
		t.Fatal("named values or cleared revision changed after reopening")
	}
	if err := restored.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAdapterRejectsLegacyMCPListWithoutChangingStoredData(t *testing.T) {
	storage, err := Open(t.Context(), filepath.Join(t.TempDir(), "settings.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	// A former experimental shape is rejected instead of guessed or erased.
	if _, err := storage.database.ExecContext(t.Context(), `INSERT INTO settings_sections VALUES ('mcp', 1, '{"servers":[]}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Load(t.Context()); !errors.Is(err, settings.ErrStorage) {
		t.Fatalf("legacy list was silently interpreted: %v", err)
	}
	var count int
	if err := storage.database.QueryRowContext(t.Context(), `SELECT count(*) FROM settings_sections WHERE namespace = 'mcp'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("failed load changed stored data")
	}
}

func storedGroup(kind settings.Section, name string) settings.Group {
	return settings.Group{
		Section: kind,
		Name:    name,
	}
}

func TestAdapterSeparatesTypesWithTheSameName(t *testing.T) {
	storage, err := Open(t.Context(), filepath.Join(t.TempDir(), "settings.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	identifier := "shared"
	persona := "named prompt"
	next := settings.State{
		Revision: 3,
		Versions: map[settings.Group]int64{
			storedGroup(settings.ProviderSection, identifier): 1,
			storedGroup(settings.PromptSection, identifier):   2,
			storedGroup(settings.ToolSection, identifier):     3,
		},
		User: settings.Values{
			Providers: map[string]settings.ProviderConfig{
				identifier: {},
			},
			Tools: map[string]tools.Config{
				identifier: {},
			},
			Prompts: map[string]settings.PromptConfig{
				identifier: {
					Persona: &persona,
				},
			},
		},
	}
	if err := storage.Commit(t.Context(), settings.ChangeSet{
		ExpectedRevision: 0,
		Next:             next,
	}); err != nil {
		t.Fatal(err)
	}
	restored, err := storage.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, next) {
		t.Fatal("same names in different types collided in storage")
	}
}
