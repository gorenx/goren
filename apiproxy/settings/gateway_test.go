package settingsapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gorenx/goren/apiproxy/rpc"
	api "github.com/gorenx/goren/apiproxy/settings"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/mcpclient"
	"github.com/gorenx/goren/settings"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

func TestSettingsGatewayReportsAbsentProvider(t *testing.T) {
	gateway := api.NewGateway(nil)
	methods := rpc.NewCatalog()
	if err := api.Register(methods, gateway); err != nil {
		t.Fatal(err)
	}
	result, err := methods.DispatchUnary(
		context.Background(), api.DescribeMethod,
		connection.RPCID("settings-absent"), json.RawMessage(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.OK ||
		result.Error == nil ||
		result.Error.Code != connection.ErrorInternal {
		t.Fatalf("result = %#v", result)
	}
	want := "settings service is absent: this deployment does not mount a settings provider (e.g. @deepseek-ai/dsh-settings-file) in its composition"
	if result.Error.Message != want || string(result.Error.Details) != `{}` {
		t.Fatalf("error = %#v", result.Error)
	}
}

type storedSettings struct {
	state   settings.State
	failure error
}

func (storage *storedSettings) Load(context.Context) (settings.State, error) {
	return storage.state.Clone(), storage.failure
}

func (storage *storedSettings) Commit(_ context.Context, change settings.ChangeSet) error {
	if storage.failure != nil {
		return storage.failure
	}
	if storage.state.Revision != change.ExpectedRevision {
		return settings.ErrConflict
	}
	storage.state = change.Next.Clone()
	return nil
}

type promptUpdater struct {
	failure error
}

func (updater *promptUpdater) ApplyPrompt(context.Context, string, systemprompt.Config) error {
	return updater.failure
}

type connectionUpdater struct{}

func (*connectionUpdater) Validate(context.Context, []mcpclient.Config) error { return nil }

func (*connectionUpdater) Reconcile(context.Context, []mcpclient.Config) error { return nil }

type observedSettings struct {
	settings.Settings
	reads int
}

func (observer *observedSettings) Describe(ctx context.Context) (settings.Snapshot, error) {
	observer.reads++
	return observer.Settings.Describe(ctx)
}

func TestSettingsGatewayEditsDomainStateAndReportsApplication(t *testing.T) {
	persona := "base persona"
	storage := &storedSettings{}
	updater := &promptUpdater{}
	business, err := settings.NewService(settings.Directory{
		Values: settings.Values{
			Prompts: map[string]settings.PromptConfig{
				"default": {
					Persona: &persona,
				},
			},
			Providers: map[string]settings.ProviderConfig{"deepseek": {}},
		},
	}, settings.State{}, storage, updater, &connectionUpdater{}, &connectionUpdater{})
	if err != nil {
		t.Fatal(err)
	}
	observer := &observedSettings{
		Settings: business,
	}
	methods := rpc.NewCatalog()
	if err := api.Register(methods, api.NewGateway(observer)); err != nil {
		t.Fatal(err)
	}
	call := func(method string, payload string) connection.RPCResult {
		t.Helper()
		result, err := methods.DispatchUnary(context.Background(), method, "settings-test", json.RawMessage(payload))
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	read := call(api.DescribeMethod, `{}`)
	var description api.DescribeValue
	if json.Unmarshal(read.Value, &description) != nil ||
		!read.OK ||
		!description.Writable ||
		description.HasDocument ||
		len(description.Namespaces) != 2 {
		t.Fatalf("description = %#v", read)
	}
	for _, entry := range description.Namespaces {
		if !json.Valid(entry.Schema) ||
			len(entry.Secrets) != 0 {
			t.Fatal("invalid or secret-bearing namespace projection")
		}
	}
	readGroup := func(method string, payload string) api.NamespaceView {
		t.Helper()
		result := call(method, payload)
		var view api.NamespaceView
		if !result.OK || json.Unmarshal(result.Value, &view) != nil {
			t.Fatalf("update = %#v", result)
		}
		return view
	}
	readGroup(api.UpdatePromptMethod, `{"name":"default","expectedRevision":0,"config":{"persona":"custom","includeHarnessIdentity":false}}`)
	second := readGroup(api.UpdatePromptMethod, `{"name":"default","expectedRevision":1,"config":{"persona":"changed"}}`)
	var overridden settings.PromptConfig
	if json.Unmarshal(*second.User, &overridden) != nil ||
		overridden.IncludeHarnessIdentity != nil {
		t.Fatal("whole replacement inherited omitted field")
	}
	third := readGroup(api.ResetPromptMethod, `{"name":"default","expectedRevision":2}`)
	var target settings.PromptConfig
	if json.Unmarshal(third.Value, &target) != nil || *target.Persona != persona {
		t.Fatal("explicit reset did not restore the local value")
	}
	fourth := readGroup(api.UpdatePromptMethod, `{"name":"default","expectedRevision":3,"config":{}}`)
	if string(*fourth.User) != `{}` || fourth.Revision != 4 {
		t.Fatal("whole group reset did not preserve revision or remove overrides")
	}
	conflict := call(api.UpdatePromptMethod, `{"name":"default","expectedRevision":0,"config":{"persona":"stale"}}`)
	if conflict.Error == nil ||
		conflict.Error.Code != connection.ErrorSettingsConflict ||
		string(conflict.Error.Details) != `{"ns":"prompt","name":"default","expected":0,"actual":4}` {
		t.Fatal("stale editor was not rejected")
	}
	secret := call(api.UpdateProviderMethod, `{"name":"deepseek","expectedRevision":0,"config":{"apiKey":"secret-marker"}}`)
	if secret.Error == nil ||
		secret.Error.Code != connection.ErrorBadRequest ||
		strings.Contains(string(secret.Error.Details), "secret-marker") {
		t.Fatal("secret path accepted")
	}
	storage.failure = settings.ErrStorage
	failed := call(api.UpdatePromptMethod, `{"name":"default","expectedRevision":4,"config":{"persona":"not saved"}}`)
	if failed.Error == nil ||
		failed.Error.Code != connection.ErrorSettingsRejected ||
		storage.state.Revision != 4 {
		t.Fatal("failed persistence was reported as saved")
	}
	storage.failure = settings.ErrUncertain
	uncertain := call(api.UpdatePromptMethod, `{"name":"default","expectedRevision":4,"config":{"persona":"uncertain"}}`)
	if uncertain.Error == nil || !strings.Contains(uncertain.Error.Message, "reload settings") {
		t.Fatalf("uncertain outcome: %#v", uncertain)
	}
	storage.failure = nil
	updater.failure = errors.New("application fixture failure")
	saved := readGroup(api.UpdatePromptMethod, `{"name":"default","expectedRevision":4,"config":{"persona":"saved"}}`)
	if saved.ApplyError == "" ||
		saved.Revision != 5 ||
		*saved.AppliedRevision != 4 {
		t.Fatal("application failure was not separated from persistence")
	}
	updater.failure = nil
	retried := readGroup(api.RetryApplicationMethod, `{"ns":"prompt","name":"default","expectedRevision":5}`)
	if retried.ApplyError != "" ||
		retried.Revision != 5 ||
		*retried.AppliedRevision != 5 {
		t.Fatal("retry did not apply the saved revision")
	}

	if observer.reads != 1 {
		t.Fatalf("updates or retry performed extra reads: %d", observer.reads)
	}
	if methods.HasUnary("settings.mutate") {
		t.Fatal("obsolete mutation route is still registered")
	}
	provider := readGroup(api.UpdateProviderMethod, `{"name":"deepseek","expectedRevision":0,"config":{"apiKeyEnv":"CUSTOM_API_KEY"}}`)
	if provider.NS != "llmprovider" || !provider.PendingRestart {
		t.Fatalf("provider result = %#v", provider)
	}
	mcp := readGroup(api.UpdateMCPMethod, `{"name":"testa","expectedRevision":0,"config":{"enabled":true,"connection":{"serverName":"testa","transport":"streamable-http","url":"https://example.test"}}}`)
	if mcp.NS != "mcp" ||
		mcp.Name != "testa" ||
		!strings.Contains(string(*mcp.User), `"enabled":true`) {
		t.Fatalf("MCP result = %#v", mcp)
	}
	providerReset := readGroup(api.ResetProviderMethod, `{"name":"deepseek","expectedRevision":6}`)
	if providerReset.User != nil || providerReset.NS != "llmprovider" {
		t.Fatalf("provider reset: %#v", providerReset)
	}
	mcpReset := readGroup(api.ResetMCPMethod, `{"name":"testa","expectedRevision":7}`)
	if mcpReset.User != nil ||
		mcpReset.NS != "mcp" ||
		mcpReset.Name != "testa" {
		t.Fatalf("MCP reset: %#v", mcpReset)
	}
	unknown := call(api.RetryApplicationMethod, `{"ns":"unknown","name":"default","expectedRevision":0}`)
	if unknown.Error == nil || unknown.Error.Code != connection.ErrorSettingsNotExposed {
		t.Fatalf("unknown group: %#v", unknown)
	}
	invalid := call(api.UpdateProviderMethod, `{"name":"deepseek","expectedRevision":8,"config":{"streamIdleTimeoutMs":-1}}`)
	if invalid.Error == nil || invalid.Error.Code != connection.ErrorSettingsRejected {
		t.Fatalf("invalid domain configuration: %#v", invalid)
	}
	if observer.reads != 1 {
		t.Fatalf("provider/MCP updates performed extra reads: %d", observer.reads)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = methods.DispatchUnary(canceled, api.UpdatePromptMethod, "canceled", json.RawMessage(`{"name":"default","expectedRevision":5,"config":{}}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func (*connectionUpdater) ValidateTool(context.Context, string) error            { return nil }
func (*connectionUpdater) ApplyTool(context.Context, string, tools.Config) error { return nil }
