package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gorenx/goren/internal/server"
	"github.com/gorenx/goren/mcpclient"
)

func main() {
	err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	settings := parseConfig()
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	configDirectory, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	paths, err := settings.resolveStorage(filepath.Join(configDirectory, "goren"))
	if err != nil {
		return err
	}
	var externalServers []mcpclient.Config
	for _, path := range settings.mcpFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		configured, err := mcpclient.DecodeConfig(data)
		if err != nil {
			return err
		}
		externalServers = append(externalServers, configured)
	}
	process, err := server.New(server.Config{
		Address:           settings.address,
		Version:           settings.version,
		WorkingDirectory:  cwd,
		SessionDatabase:   paths.sessionDatabase,
		WorkspaceDatabase: paths.workspaceDatabase,
		CredentialsPath:   filepath.Join(filepath.Dir(paths.sessionDatabase), ".credentials.json"),
		MCP:               externalServers,
	}, processEnvironment{})
	if err != nil {
		return err
	}
	lifetime, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = process.Start(lifetime)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Goren Agent started\n  Web: http://%s\n  Workspace: %s\n  Data: %s\n", process.Address(), cwd, paths.dataDirectory)
	var failure error
	select {
	case <-lifetime.Done():
	case failure = <-process.Done():
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return errors.Join(failure, process.Close(cleanup))
}

type processEnvironment struct{}

func (processEnvironment) Lookup(name string) (string, bool) { return os.LookupEnv(name) }
func (processEnvironment) UserHomeDir() (string, error)      { return os.UserHomeDir() }
