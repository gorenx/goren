package local_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gorenx/goren/settings/local"
)

func writeConfig(t *testing.T, directory string, name string, data string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExamplesAreEquivalentAndPreserveRelativePaths(t *testing.T) {
	yamlInput, err := local.LoadFile("../../goren.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	jsonInput, err := local.LoadFile("../../goren.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(yamlInput, jsonInput) {
		t.Fatal("YAML and JSON examples differ")
	}
	if !filepath.IsAbs(*yamlInput.Storage.DataDirectory) {
		t.Fatal("file-relative storage path was not resolved")
	}
}

func TestSourcesMergeByPresenceBeforeStorageDefaults(t *testing.T) {
	path := writeConfig(t, t.TempDir(), "input.yaml", `schemaVersion: 1
server:
  listen: "127.0.0.1:3081"
storage:
  dataDirectory: ./file-data
llm:
  deepseek:
    apiKeyEnv: CUSTOM_KEY
    baseURL: https://file.example
prompt:
  includeRuntimeContext: false
  persona: ""
mcp:
  files: []
`)
	environment := map[string]string{
		"GOREN_LISTEN":      "127.0.0.1:3082",
		"GOREN_DATA_DIR":    "environment-data",
		"DEEPSEEK_BASE_URL": "https://environment.example",
	}
	listen := "127.0.0.1:3083"
	overrides := local.Config{
		Server: local.ServerConfig{
			Listen: &listen,
		},
	}
	input, err := local.Load(path, overrides, func(name string) (string, bool) {
		value, found := environment[name]
		return value, found
	})
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if *input.Server.Listen != listen ||
		*input.LLM.DeepSeek.BaseURL != environment["DEEPSEEK_BASE_URL"] ||
		*input.LLM.DeepSeek.APIKeyEnv != "CUSTOM_KEY" {
		t.Fatal("source precedence or sibling-field preservation failed")
	}
	if input.Prompt.IncludeRuntimeContext == nil ||
		*input.Prompt.IncludeRuntimeContext ||
		input.Prompt.Persona == nil ||
		*input.Prompt.Persona != "" {
		t.Fatal("explicit false or empty text was lost")
	}
	wantDirectory := filepath.Join(cwd, "environment-data")
	if *input.Storage.DataDirectory != wantDirectory || *input.Storage.SessionDatabase != filepath.Join(wantDirectory, "sessions.sqlite") {
		t.Fatal("storage defaults were derived before final precedence")
	}
	if input.Server.Listen == overrides.Server.Listen {
		t.Fatal("resolved input aliases caller-owned fields")
	}
	if _, err := os.Stat(wantDirectory); !os.IsNotExist(err) {
		t.Fatal("configuration loading created a storage directory")
	}
}

func TestFilesRejectAmbiguousOrInvalidInputWithoutLeakingValues(t *testing.T) {
	for _, entry := range []struct {
		name string
		data string
	}{
		{
			name: "unknown.yaml",
			data: "schemaVersion: 1\nsecret: secret-marker",
		},
		{
			name: "case.yaml",
			data: "schemaVersion: 1\nprompt: {Persona: secret-marker}",
		},
		{
			name: "duplicate.yaml",
			data: "schemaVersion: 1\nprompt: {persona: first, persona: secret-marker}",
		},
		{
			name: "null.yaml",
			data: "schemaVersion: 1\nprompt: {persona: null}",
		},
		{
			name: "tag.yaml",
			data: "schemaVersion: 1\nprompt: {persona: !!str secret-marker}",
		},
		{
			name: "alias.yaml",
			data: "schemaVersion: 1\nprompt: {persona: &text secret-marker}",
		},
		{
			name: "merge.yaml",
			data: "schemaVersion: 1\nprompt: {<<: {persona: secret-marker}}",
		},
		{
			name: "documents.yaml",
			data: "schemaVersion: 1\n---\nsecret-marker",
		},
		{
			name: "type.yaml",
			data: "schemaVersion: 1\nprompt: {includeRuntimeContext: 'false'}",
		},
		{
			name: "integer.yaml",
			data: "schemaVersion: 1.0",
		},
		{
			name: "date.yaml",
			data: "schemaVersion: 1\nprompt: {persona: 2026-09-11}",
		},
		{
			name: "number-key.yaml",
			data: "schemaVersion: 1\nprompt: {1: secret-marker}",
		},
		{
			name: "infinity.yaml",
			data: "schemaVersion: 1\nllm: {deepseek: {streamIdleTimeoutMs: .inf}}",
		},
		{
			name: "bool.yaml",
			data: "schemaVersion: 1\nprompt: {includeRuntimeContext: True}",
		},
		{
			name: "empty.yaml",
			data: "",
		},
		{
			name: "version.yaml",
			data: "schemaVersion: 2",
		},
		{
			name: "absent-version.yaml",
			data: "{}",
		},
		{
			name: "timeout.yaml",
			data: "schemaVersion: 1\nserver: {requestTimeoutMs: 0}",
		},
		{
			name: "invalid.json",
			data: `{"schemaVersion":1,"prompt":{"persona":"first","persona":"secret-marker"}}`,
		},
		{
			name: "case.json",
			data: `{"schemaVersion":1,"prompt":{"Persona":"secret-marker"}}`,
		},
		{
			name: "null.json",
			data: `{"schemaVersion":1,"prompt":{"persona":null}}`,
		},
		{
			name: "trailing.json",
			data: `{"schemaVersion":1} {}`,
		},
		{
			name: "integer.json",
			data: `{"schemaVersion":1.0}`,
		},
		{
			name: "yaml-in.json",
			data: "schemaVersion: 1",
		},
		{
			name: "unknown.toml",
			data: "schemaVersion: 1",
		},
	} {
		t.Run(entry.name, func(t *testing.T) {
			path := writeConfig(t, t.TempDir(), entry.name, entry.data)
			_, err := local.LoadFile(path)
			if err == nil {
				t.Fatal("invalid input accepted")
			}
			if strings.Contains(err.Error(), "secret-marker") {
				t.Fatal("diagnostic exposed input")
			}
		})
	}
}

func TestHigherPrecedenceCannotHideInvalidSource(t *testing.T) {
	valid := "127.0.0.1:3080"
	path := writeConfig(t, t.TempDir(), "input", "schemaVersion: 1\nserver: {listen: invalid}")
	overrides := local.Config{
		Server: local.ServerConfig{
			Listen: &valid,
		},
	}
	if _, err := local.Load(path, overrides, nil); err == nil {
		t.Fatal("invalid file value was hidden by CLI")
	}
	if _, err := local.Load("", overrides, func(name string) (string, bool) {
		return "", name == "GOREN_LISTEN"
	}); err == nil {
		t.Fatal("empty environment value was hidden by CLI")
	}
}

func TestIndependentMCPFilesUseFrameworkDecodersAndRejectDuplicateNames(t *testing.T) {
	directory := t.TempDir()
	writeConfig(t, directory, "external.yaml", "serverName: fixture\ntransport: streamable-http\nurl: http://127.0.0.1:1/mcp")
	path := writeConfig(t, directory, "input.yaml", "schemaVersion: 1\nmcp: {files: [external.yaml]}")
	input, err := local.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := input.Connections()
	if err != nil || len(entries) != 1 {
		t.Fatalf("MCP definitions: %v %v", entries, err)
	}
	input.MCP.Servers = &entries
	if _, err := input.Connections(); err == nil {
		t.Fatal("duplicate inline and file name was accepted")
	}
}

func TestNamedToolConfigurationUsesStrictFrameworkDecoding(t *testing.T) {
	for _, format := range []string{"yaml", "json"} {
		t.Run(format, func(t *testing.T) {
			data := `{"schemaVersion":1,"tools":{"ask_user_question":{"enabled":true,"visible":false}}}`
			if format == "yaml" {
				data = "schemaVersion: 1\ntools:\n  ask_user_question:\n    enabled: true\n    visible: false\n"
			}
			path := writeConfig(t, t.TempDir(), "goren."+format, data)
			configuration, err := local.LoadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			enabled, visible := configuration.Tools["ask_user_question"].Resolve()
			if !enabled || visible {
				t.Fatal("tool flags were lost")
			}
		})
	}
	for _, data := range []string{
		"schemaVersion: 1\ntools: {ask_user_question: {Visible: false}}",
		"schemaVersion: 1\ntools: {ask_user_question: {enabled: 'false'}}",
		"schemaVersion: 1\ntools: {ask_user_question: null}",
		"schemaVersion: 1\ntools: {1: {enabled: true}}",
		"schemaVersion: 1\ntools: {ask_user_question: {enabled: true, enabled: false}}",
		"schemaVersion: 1\ntools: {ask_user_question: {guard: secret-marker}}",
	} {
		path := writeConfig(t, t.TempDir(), "goren.yaml", data)
		if _, err := local.LoadFile(path); err == nil || strings.Contains(err.Error(), "secret-marker") {
			t.Fatal("invalid tool configuration accepted or echoed")
		}
	}
}
