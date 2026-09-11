package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/llm/deepseek"
	"github.com/gorenx/goren/settings"
	"github.com/gorenx/goren/settings/local"
)

type environment struct {
	directory string
}

func (source environment) Lookup(string) (string, bool) { return "", false }
func (source environment) UserHomeDir() (string, error) { return source.directory, nil }

func TestFileConfigurationReachesRealServer(t *testing.T) {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "GOREN_") || name == "DEEPSEEK_BASE_URL" {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}

	for _, format := range []string{"yaml", "json"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "goren."+format)
			data := `{"schemaVersion":1,"server":{"listen":"127.0.0.1:0"},"storage":{"dataDirectory":"./data"},"llm":{"default":{"model":"fixture-model"}},"prompt":{"persona":"local persona"}}`
			if format == "yaml" {
				data = "schemaVersion: 1\nserver: {listen: '127.0.0.1:0'}\nstorage: {dataDirectory: ./data}\nllm: {default: {model: fixture-model}}\nprompt: {persona: local persona}\n"
			}
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			input, err := local.Load(path, local.Config{})
			if err != nil {
				t.Fatal(err)
			}
			configured, err := ConfigFromLocal(input, root, "config-test")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			platform := environment{
				directory: root,
			}
			if err := configured.Inspect(ctx, platform); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(root, "data")); !os.IsNotExist(err) {
				t.Fatal("preflight created storage")
			}
			if configured.Prompt.Persona != "local persona" {
				t.Fatal("prompt input was dropped during composition")
			}
			process, err := New(configured, platform)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				if err := process.Close(cleanup); err != nil {
					t.Error(err)
				}
			})
			if err := process.Start(ctx); err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(connection.ClientRequest{
				Type:    "client-request",
				RPCID:   "configuration",
				Method:  "host.describe",
				Payload: json.RawMessage(`{}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+process.Address()+"/api/host.describe", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var envelope connection.ServerResponse
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			var description apiproxy.HostDescription
			if json.Unmarshal(envelope.Result.Value, &description) != nil ||
				!envelope.Result.OK ||
				description.Model != "fixture-model" ||
				description.Version != "config-test" {
				t.Fatalf("configuration did not reach Host API: %#v", envelope)
			}
		})
	}
}

func TestProviderWholeGroupOmissionUsesOwnerDefaults(t *testing.T) {
	root := t.TempDir()
	platform := settingsEnvironment{
		home:     root,
		endpoint: "https://environment.example.test",
	}
	configuration := Config{
		WorkingDirectory:  root,
		SessionDatabase:   filepath.Join(root, "session.sqlite"),
		WorkspaceDatabase: filepath.Join(root, "workspace.sqlite"),
		CredentialsPath:   filepath.Join(root, "credentials.json"),
	}
	baseline, err := configuration.Validate(platform)
	if err != nil {
		t.Fatal(err)
	}
	if *baseline.Provider.BaseURL != platform.endpoint {
		t.Fatal("fixture did not select environment baseline")
	}
	target, err := settings.Resolve(baseline.userBaseline(), settings.Directory{
		Values: settings.Values{
			Providers: map[string]settings.ProviderConfig{
				"deepseek": {},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := baseline.withUserSettings(target).Validate(platform)
	if err != nil {
		t.Fatal(err)
	}
	if *selected.Provider.BaseURL != deepseek.PublicBaseURL {
		t.Fatal("empty complete group inherited the environment endpoint")
	}
}

type settingsEnvironment struct {
	home     string
	endpoint string
}

func (source settingsEnvironment) Lookup(name string) (string, bool) {
	if name == deepseek.BaseURLEnv {
		return source.endpoint, true
	}
	return "", false
}

func (source settingsEnvironment) UserHomeDir() (string, error) { return source.home, nil }
