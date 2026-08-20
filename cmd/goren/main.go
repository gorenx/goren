package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gorenx/goren/internal/assembly"
	"github.com/gorenx/goren/internal/llm/deepseek"
	"github.com/gorenx/goren/plugin"
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

func main() {
	settings := parseConfig()
	workingDirectory, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve working directory:", err)
		os.Exit(1)
	}

	registry, err := assembly.NewCatalog(assembly.Environment{WorkingDirectory: workingDirectory})
	if err != nil {
		fmt.Fprintln(os.Stderr, "create factory catalog:", err)
		os.Exit(1)
	}
	configDirectory, err := os.UserConfigDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve user config directory:", err)
		os.Exit(1)
	}
	paths, err := settings.resolveStorage(filepath.Join(configDirectory, "goren"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve data storage paths:", err)
		os.Exit(1)
	}
	declarations, err := assembly.DefaultSpecs(
		settings.address, settings.version, paths.sessionDatabase, paths.workspaceDatabase,
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create server composition:", err)
		os.Exit(1)
	}

	lifecycle, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	engine := plugin.NewRuntime()
	if _, err := assembly.Load(lifecycle, engine, registry, declarations); err != nil {
		fmt.Fprintln(os.Stderr, "start server composition:", err)
		os.Exit(1)
	}
	credentialPath := filepath.Join(filepath.Dir(paths.sessionDatabase), ".credentials.json")
	fmt.Fprintf(os.Stdout, "Goren Agent started\n  Web: http://%s\n  Workspace: %s\n  Data: %s\n  Session DB: %s\n  Workspace DB: %s\n  Model: %s / %s\n  Credentials: %s (environment %s takes precedence)\n",
		settings.address, workingDirectory, paths.dataDirectory, paths.sessionDatabase, paths.workspaceDatabase,
		deepseek.ProviderRoute, deepseek.DefaultModelID, credentialPath, deepseek.DefaultAPIKeyEnv,
	)
	<-lifecycle.Done()
	closeContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := engine.Shutdown(closeContext); err != nil {
		fmt.Fprintln(os.Stderr, "stop server composition:", err)
		os.Exit(1)
	}
}

func parseConfig() commandConfig {
	address := flag.String("listen", "127.0.0.1:3080", "Echo listen address")
	version := flag.String("version", "dev", "host.describe version")
	dataDirectory := flag.String("data-dir", "", "data storage directory (default: user config directory)")
	sessionDatabase := flag.String("session-db", "", "SQLite Session database path (default: <data-dir>/sessions.sqlite)")
	workspaceDatabase := flag.String("workspace-db", "", "SQLite Workspace database path (default: <data-dir>/workspaces.sqlite)")
	flag.Parse()
	return commandConfig{
		address: *address, version: *version,
		dataDirectory: *dataDirectory, sessionDatabase: *sessionDatabase,
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
		return storagePaths{}, fmt.Errorf("Session database: %w", err)
	}
	workspaceDatabase, err := resolveStoragePath(settings.workspaceDatabase, filepath.Join(dataDirectory, "workspaces.sqlite"))
	if err != nil {
		return storagePaths{}, fmt.Errorf("Workspace database: %w", err)
	}
	return storagePaths{
		dataDirectory: filepath.Clean(dataDirectory), sessionDatabase: sessionDatabase,
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
