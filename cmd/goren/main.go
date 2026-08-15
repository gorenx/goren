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
	"github.com/gorenx/goren/internal/llmdeepseek"
	"github.com/gorenx/goren/plugin"
)

type commandConfig struct {
	address           string
	version           string
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
	sessionDatabase := settings.sessionDatabase
	workspaceDatabase := settings.workspaceDatabase
	if sessionDatabase == "" {
		configDirectory, configErr := os.UserConfigDir()
		if configErr != nil {
			fmt.Fprintln(os.Stderr, "resolve user config directory:", configErr)
			os.Exit(1)
		}
		sessionDatabase = filepath.Join(configDirectory, "goren", "sessions.sqlite")
		if workspaceDatabase == "" {
			workspaceDatabase = filepath.Join(configDirectory, "goren", "workspaces.sqlite")
		}
	} else if workspaceDatabase == "" {
		workspaceDatabase = filepath.Join(filepath.Dir(sessionDatabase), "workspaces.sqlite")
	}
	declarations, err := assembly.DefaultSpecs(
		settings.address, settings.version, sessionDatabase, workspaceDatabase,
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
	credentialPath := filepath.Join(filepath.Dir(sessionDatabase), ".credentials.json")
	fmt.Fprintf(os.Stdout, "Goren Agent started\n  Web: http://%s\n  Workspace: %s\n  Model: %s / %s\n  Credentials: %s (environment %s takes precedence)\n",
		settings.address, workingDirectory, llmdeepseek.ProviderRoute, llmdeepseek.DefaultModelID,
		credentialPath, llmdeepseek.DefaultAPIKeyEnv,
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
	sessionDatabase := flag.String("session-db", "", "SQLite Session database path (default: user config directory)")
	workspaceDatabase := flag.String("workspace-db", "", "SQLite Workspace database path (default: beside Session database)")
	flag.Parse()
	return commandConfig{
		address: *address, version: *version,
		sessionDatabase: *sessionDatabase, workspaceDatabase: *workspaceDatabase,
	}
}
