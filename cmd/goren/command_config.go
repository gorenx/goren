package main

import (
	"errors"
	"flag"
	"io"

	"github.com/gorenx/goren/settings/local"
)

var errArguments = errors.New("goren: invalid command arguments; use --help")

type commandConfig struct {
	path      string
	version   string
	check     bool
	overrides local.Config
}

func parseConfig(arguments []string) (commandConfig, error) {
	options := commandConfig{
		version: "dev",
	}
	flags := flag.NewFlagSet("goren", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	seenPath := false
	flags.Func("config", "YAML or JSON configuration path", func(value string) error {
		if seenPath || value == "" {
			return errArguments
		}
		seenPath = true
		options.path = value
		return nil
	})
	flags.BoolVar(&options.check, "check-config", false, "validate configuration without starting services")
	flags.StringVar(&options.version, "version", "dev", "host.describe version")
	for _, entry := range []struct {
		name  string
		help  string
		field **string
	}{
		{
			name:  "listen",
			help:  "Echo listen address",
			field: &options.overrides.Server.Listen,
		},
		{
			name:  "data-dir",
			help:  "data storage directory",
			field: &options.overrides.Storage.DataDirectory,
		},
		{
			name:  "session-db",
			help:  "SQLite Session database path",
			field: &options.overrides.Storage.SessionDatabase,
		},
		{
			name:  "workspace-db",
			help:  "SQLite Workspace database path",
			field: &options.overrides.Storage.WorkspaceDatabase,
		},
		{
			name:  "settings-db",
			help:  "SQLite user settings path",
			field: &options.overrides.Settings.Database,
		},
	} {
		flags.Func(entry.name, entry.help, func(value string) error {
			if value == "" {
				return errArguments
			}
			*entry.field = &value
			return nil
		})
	}
	files := []string{}
	flags.Func("mcp-server", "MCP server YAML or JSON config path (repeatable)", func(value string) error {
		if value == "" {
			return errArguments
		}
		files = append(files, value)
		options.overrides.MCP.Files = &files
		return nil
	})
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return commandConfig{}, flag.ErrHelp
		}
		return commandConfig{}, errArguments
	}
	if flags.NArg() != 0 {
		return commandConfig{}, errArguments
	}
	return options, nil
}

const commandHelp = `Usage: goren [options]
  --config PATH       YAML or JSON configuration (YAML by default)
  --check-config      validate without starting services
  --listen ADDRESS   override the listen address
  --data-dir PATH    override the data directory
  --session-db PATH  override the Session database
  --workspace-db PATH override the Workspace database
  --settings-db PATH override the user settings database
  --mcp-server PATH  MCP YAML/JSON definition (repeatable)
  --version VALUE    host.describe version (default dev)
`
