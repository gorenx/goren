package main

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorenx/goren/settings/local"
)

func TestCommandConfigSuppliesOnlyExplicitOverrides(t *testing.T) {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "GOREN_") || name == "DEEPSEEK_BASE_URL" {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}

	options, err := parseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if options.path != "" ||
		options.overrides.Server.Listen != nil ||
		options.overrides.MCP.Files != nil {
		t.Fatal("absent flags created overrides")
	}
	root := t.TempDir()
	sessionPath := filepath.Join(root, "custom", "sessions.sqlite")
	workspacePath := filepath.Join(root, "custom", "workspaces.sqlite")
	options, err = parseConfig([]string{"--data-dir", root, "--session-db", sessionPath, "--workspace-db", workspacePath, "--listen", "127.0.0.1:3000", "--listen", "127.0.0.1:3001", "--mcp-server", "a.yaml", "--mcp-server", "b.json"})
	if err != nil {
		t.Fatal(err)
	}
	input, err := local.Load(options.path, options.overrides)
	if err != nil {
		t.Fatal(err)
	}
	if *input.Storage.DataDirectory != root ||
		*input.Storage.SessionDatabase != sessionPath ||
		*input.Storage.WorkspaceDatabase != workspacePath ||
		*input.Server.Listen != "127.0.0.1:3001" {
		t.Fatal("explicit command overrides were not preserved")
	}
	if len(*input.MCP.Files) != 2 {
		t.Fatal("repeatable MCP flags were lost")
	}
}

func TestCommandConfigAcceptsPathWithoutReadingIt(t *testing.T) {
	options, err := parseConfig([]string{"--config", "missing.yaml", "--check-config"})
	if err != nil ||
		options.path != "missing.yaml" ||
		!options.check {
		t.Fatalf("options = %#v, %v", options, err)
	}
}

func TestCommandConfigRejectsInvalidArguments(t *testing.T) {
	for _, arguments := range [][]string{
		{"--config", ""},
		{"--config", "one.yaml", "--config", "two.json"},
		{"--config"},
		{"--unknown"},
		{"--data-dir", ""},
		{"--mcp-server", ""},
		{"position.yaml"},
	} {
		if _, err := parseConfig(arguments); !errors.Is(err, errArguments) {
			t.Fatalf("arguments %v: %v", arguments, err)
		}
	}
	if _, err := parseConfig([]string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help: %v", err)
	}
}
