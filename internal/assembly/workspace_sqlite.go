package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/workspace"
	workspaceSqlite "github.com/gorenx/goren/workspace/persistence/sqlite"
)

// WorkspaceSQLiteConfig configures the Workspace data adapter.
type WorkspaceSQLiteConfig struct {
	Path        string                      `json:"path"`
	JournalMode workspaceSqlite.JournalMode `json:"journalMode,omitempty"`
}

type workspaceSQLiteFactory struct{}

func (workspaceSQLiteFactory) Name() string {
	return WorkspaceSQLiteFactoryName
}

func (workspaceSQLiteFactory) DecodeConfig(
	rawConfig json.RawMessage,
) (WorkspaceSQLiteConfig, error) {
	settings, err := plugin.DecodeStrictConfig(rawConfig, func(candidate WorkspaceSQLiteConfig) error {
		if strings.TrimSpace(candidate.Path) == "" {
			return errors.New("path must be non-empty")
		}
		switch candidate.JournalMode {
		case "", workspaceSqlite.JournalWAL, workspaceSqlite.JournalDelete,
			workspaceSqlite.JournalTruncate, workspaceSqlite.JournalPersist:
		default:
			return errors.New("journalMode must be wal, delete, truncate, or persist")
		}
		return nil
	})
	if err != nil {
		return WorkspaceSQLiteConfig{}, err
	}
	if settings.JournalMode == "" {
		settings.JournalMode = workspaceSqlite.JournalWAL
	}
	return settings, nil
}

func (workspaceSQLiteFactory) New(
	_ context.Context,
	settings WorkspaceSQLiteConfig,
) (plugin.Plugin, error) {
	return &workspaceSQLitePlugin{settings: settings}, nil
}

type workspaceSQLitePlugin struct {
	settings WorkspaceSQLiteConfig
}

func (*workspaceSQLitePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:     WorkspaceSQLiteFactoryName,
		Provides: []plugin.ServiceRef{workspace.BackendService.Ref()},
	}
}

func (instance *workspaceSQLitePlugin) Apply(
	requestContext context.Context,
	pluginScope *plugin.Scope,
) error {
	storage, err := workspaceSqlite.Open(requestContext, workspaceSqlite.Config{
		Path: instance.settings.Path, JournalMode: instance.settings.JournalMode,
	})
	if err != nil {
		return err
	}
	if err := pluginScope.Effect(requestContext, "workspacePersistence.close()",
		func(context.Context) (plugin.Disposer, error) {
			return storage.Close, nil
		}); err != nil {
		_ = storage.Close(requestContext)
		return err
	}
	_, err = plugin.Provide(pluginScope, workspace.BackendService, workspace.Backend(storage))
	return err
}
