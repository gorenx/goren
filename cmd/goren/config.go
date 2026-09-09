package main

import (
	"flag"
	"fmt"
	"path/filepath"
)

type commandConfig struct {
	address           string
	version           string
	dataDirectory     string
	sessionDatabase   string
	workspaceDatabase string
}

type storagePaths struct {
	dataDirectory     string
	sessionDatabase   string
	workspaceDatabase string
}

func parseConfig() commandConfig {
	address := flag.String("listen", "127.0.0.1:3080", "Echo listen address")
	version := flag.String("version", "dev", "host.describe version")
	dataDirectory := flag.String("data-dir", "", "data storage directory (default: user config directory)")
	sessionDatabase := flag.String("session-db", "", "SQLite Session database path (default: <data-dir>/sessions.sqlite)")
	workspaceDatabase := flag.String("workspace-db", "", "SQLite Workspace database path (default: <data-dir>/workspaces.sqlite)")
	flag.Parse()
	return commandConfig{
		address:           *address,
		version:           *version,
		dataDirectory:     *dataDirectory,
		sessionDatabase:   *sessionDatabase,
		workspaceDatabase: *workspaceDatabase,
	}
}

func (settings commandConfig) resolveStorage(defaultDataDirectory string) (storagePaths, error) {
	dataDirectory := settings.dataDirectory
	if dataDirectory == "" {
		dataDirectory = defaultDataDirectory
	}
	dataDirectory, err := filepath.Abs(dataDirectory)
	if err != nil {
		return storagePaths{}, fmt.Errorf("data directory: %w", err)
	}
	sessionDatabase, err := resolveStoragePath(settings.sessionDatabase, filepath.Join(dataDirectory, "sessions.sqlite"))
	if err != nil {
		return storagePaths{}, fmt.Errorf("session database: %w", err)
	}
	workspaceDatabase, err := resolveStoragePath(settings.workspaceDatabase, filepath.Join(dataDirectory, "workspaces.sqlite"))
	if err != nil {
		return storagePaths{}, fmt.Errorf("workspace database: %w", err)
	}
	return storagePaths{
		dataDirectory:     filepath.Clean(dataDirectory),
		sessionDatabase:   sessionDatabase,
		workspaceDatabase: workspaceDatabase,
	}, nil
}

func resolveStoragePath(configuredPath string, fallbackPath string) (string, error) {
	selectedPath := configuredPath
	if selectedPath == "" {
		selectedPath = fallbackPath
	}
	absolutePath, err := filepath.Abs(selectedPath)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolutePath), nil
}
