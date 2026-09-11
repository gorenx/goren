package local_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gorenx/goren/settings/local"
	"github.com/gorenx/goren/tools"
)

func writeConfig(t *testing.T, directory string, name string, data string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func isolateConfigEnvironment(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "GOREN_") || name == "DEEPSEEK_BASE_URL" {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}
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
	isolateConfigEnvironment(t)
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
	for name, value := range environment {
		t.Setenv(name, value)
	}
	input, err := local.Load(path, overrides)
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

func TestFilesRejectInvalidInputWithoutLeakingValues(t *testing.T) {
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
			name: "type.yaml",
			data: "schemaVersion: 1\nprompt: {includeRuntimeContext: 'false'}",
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
			name: "case.json",
			data: `{"schemaVersion":1,"prompt":{"Persona":"secret-marker"}}`,
		},
		{
			name: "trailing.json",
			data: `{"schemaVersion":1} {}`,
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

func TestFilesDecodeThroughKoanf(t *testing.T) {
	for _, entry := range []struct {
		name    string
		data    string
		persona string
	}{
		{
			name:    "null.yaml",
			data:    "schemaVersion: 1\nprompt: {persona: null}",
			persona: "",
		},
		{
			name:    "tag.yaml",
			data:    "schemaVersion: 1\nprompt: {persona: !!str secret-marker}",
			persona: "secret-marker",
		},
		{
			name:    "alias.yaml",
			data:    "schemaVersion: 1\nprompt: {persona: &text secret-marker}",
			persona: "secret-marker",
		},
		{
			name:    "merge.yaml",
			data:    "schemaVersion: 1\nprompt: {<<: {persona: secret-marker}}",
			persona: "secret-marker",
		},
		{
			name:    "documents.yaml",
			data:    "schemaVersion: 1\n---\nsecret-marker",
			persona: "",
		},
		{
			name:    "integer.yaml",
			data:    "schemaVersion: 1.0",
			persona: "",
		},
		{
			name:    "bool.yaml",
			data:    "schemaVersion: 1\nprompt: {includeRuntimeContext: True}",
			persona: "",
		},
		{
			name:    "duplicate.json",
			data:    `{"schemaVersion":1,"prompt":{"persona":"first","persona":"secret-marker"}}`,
			persona: "secret-marker",
		},
		{
			name:    "null.json",
			data:    `{"schemaVersion":1,"prompt":{"persona":null}}`,
			persona: "",
		},
		{
			name:    "integer.json",
			data:    `{"schemaVersion":1.0}`,
			persona: "",
		},
	} {
		t.Run(entry.name, func(t *testing.T) {
			path := writeConfig(t, t.TempDir(), entry.name, entry.data)
			input, err := local.LoadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if input.SchemaVersion == nil || *input.SchemaVersion != 1 {
				t.Fatal("schema version was not decoded")
			}
			if entry.persona != "" {
				if input.Prompt.Persona == nil || *input.Prompt.Persona != entry.persona {
					t.Fatal("persona was not decoded")
				}
			} else if input.Prompt.Persona != nil {
				t.Fatal("absent persona was populated")
			}
			if entry.name == "bool.yaml" && (input.Prompt.IncludeRuntimeContext == nil || !*input.Prompt.IncludeRuntimeContext) {
				t.Fatal("YAML boolean was not decoded")
			}
		})
	}
}

func TestHigherPrecedenceCannotHideInvalidSource(t *testing.T) {
	isolateConfigEnvironment(t)
	valid := "127.0.0.1:3080"
	path := writeConfig(t, t.TempDir(), "input", "schemaVersion: 1\nserver: {listen: invalid}")
	overrides := local.Config{
		Server: local.ServerConfig{
			Listen: &valid,
		},
	}
	if _, err := local.Load(path, overrides); err == nil {
		t.Fatal("invalid file value was hidden by CLI")
	}
	t.Setenv("GOREN_LISTEN", "")
	if _, err := local.Load("", overrides); err == nil {
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
		"schemaVersion: 1\ntools: {ask_user_question: {enabled: true, enabled: false}}",
		"schemaVersion: 1\ntools: {ask_user_question: {guard: secret-marker}}",
	} {
		path := writeConfig(t, t.TempDir(), "goren.yaml", data)
		if _, err := local.LoadFile(path); err == nil || strings.Contains(err.Error(), "secret-marker") {
			t.Fatal("invalid tool configuration accepted or echoed")
		}
	}
}

func TestFilePathsUseSymlinkLocationAndExplicitPathsDoNotNeedHome(t *testing.T) {
	isolateConfigEnvironment(t)
	root := t.TempDir()
	original := filepath.Join(root, "original")
	linked := filepath.Join(root, "linked")
	for _, directory := range []string{original, linked} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	path := writeConfig(t, original, "input.yaml", "schemaVersion: 1\nstorage: {dataDirectory: ./data, sessionDatabase: ./session/custom.sqlite}\nmcp: {files: [./external.yaml]}\n")
	link := filepath.Join(linked, "input.yaml")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	input, err := local.Load(link, local.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if *input.Storage.DataDirectory != filepath.Join(linked, "data") ||
		*input.Storage.SessionDatabase != filepath.Join(linked, "session", "custom.sqlite") ||
		*input.Storage.QueryDatabase != filepath.Join(linked, "session", "query.sqlite") ||
		(*input.MCP.Files)[0] != filepath.Join(linked, "external.yaml") {
		t.Fatal("file paths used the symlink target or ignored an explicit database")
	}
	commandPath := filepath.Join("relative-cli", "sessions.sqlite")
	input, err = local.Load(link, local.Config{
		Storage: local.StorageConfig{
			SessionDatabase: &commandPath,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if *input.Storage.SessionDatabase != filepath.Join(cwd, commandPath) ||
		*input.Storage.QueryDatabase != filepath.Join(cwd, "relative-cli", "query.sqlite") ||
		*input.Storage.DataDirectory != filepath.Join(linked, "data") {
		t.Fatal("CLI path did not use process cwd independently of file paths")
	}
}

func TestMissingFileAndRelativeMCPCWDRemainInvalid(t *testing.T) {
	root := t.TempDir()
	if _, err := local.LoadFile(filepath.Join(root, "missing.yaml")); err == nil {
		t.Fatal("missing configuration file was silently ignored")
	}
	path := writeConfig(t, root, "input.yaml", "schemaVersion: 1\nmcp: {servers: [{serverName: local, transport: stdio, command: fixture, cwd: ./relative}]}\n")
	if _, err := local.LoadFile(path); err == nil {
		t.Fatal("local input bypassed MCP absolute cwd validation")
	}
}

func TestKoanfOverridesPreservePresenceAndDetachInputs(t *testing.T) {
	isolateConfigEnvironment(t)
	path := writeConfig(t, t.TempDir(), "input.yaml", `schemaVersion: 1
llm:
  default:
    maxTokens: 9007199254740993
    stop: [file-stop]
tools:
  mcp.fixture:
    enabled: true
    visible: true
`)
	directory := "command-data"
	files := []string{"command-mcp.yaml"}
	empty := []string{}
	hidden := false
	overrides := local.Config{
		Storage: local.StorageConfig{
			DataDirectory: &directory,
		},
		LLM: local.LLMConfig{
			Default: local.ModelConfig{
				Stop: &empty,
			},
		},
		MCP: local.MCPFiles{
			Files: &files,
		},
		Tools: map[string]tools.Config{
			"mcp.fixture": {
				Visible: &hidden,
			},
		},
	}
	input, err := local.Load(path, overrides)
	if err != nil {
		t.Fatal(err)
	}
	if *input.LLM.Default.MaxTokens != 9007199254740993 {
		t.Fatal("integer precision was lost while loading sources")
	}
	if input.LLM.Default.Stop == nil || len(*input.LLM.Default.Stop) != 0 {
		t.Fatal("explicit empty arrays did not replace file values")
	}
	entry, exists := input.Tools["mcp.fixture"]
	if !exists || entry.Enabled == nil || !*entry.Enabled || entry.Visible == nil || *entry.Visible {
		t.Fatal("named tool fields were not overridden independently")
	}
	if directory != "command-data" || files[0] != "command-mcp.yaml" {
		t.Fatal("path resolution modified caller input")
	}
	if !filepath.IsAbs(*input.Storage.DataDirectory) || !filepath.IsAbs((*input.MCP.Files)[0]) {
		t.Fatal("command paths were not resolved")
	}
	*entry.Visible = true
	(*input.MCP.Files)[0] = "changed"
	if hidden || files[0] != "command-mcp.yaml" {
		t.Fatal("resolved configuration retained caller-owned pointers")
	}
}
