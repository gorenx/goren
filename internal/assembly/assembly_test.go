package assembly

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/apiproxy"
	protocol "github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	toolscore "github.com/gorenx/goren/tools"
)

type probePlugin struct {
	body func(context.Context, *plugin.Scope) error
}

func (instance probePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "assembly-probe",
		Requires: []plugin.ServiceRef{
			serverServiceKey.Ref(), session.StoreService.Ref(), systemprompt.Service.Ref(), toolscore.Service.Ref(),
		},
	}
}

func (instance probePlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	return instance.body(requestContext, pluginScope)
}

func TestCatalogContainsOnlyCurrentServerSlice(t *testing.T) {
	t.Parallel()
	registry, err := NewCatalog(Environment{WorkingDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{ConnectionFactoryName, APIProxyFactoryName, SessionFactoryName, SystemPromptFactoryName, ToolsFactoryName}
	if got := registry.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("factory names = %#v, want %#v", got, want)
	}
	for _, excludedFactory := range []string{
		"@deepseek-ai/dsh-client", "@deepseek-ai/dsh-sdk", "@deepseek-ai/dsh-host-frontend-static",
		"@deepseek-ai/dsh-acp", "@deepseek-ai/dsh-mcp-client",
	} {
		if _, err := registry.Create(context.Background(), excludedFactory, json.RawMessage(`{}`)); err == nil {
			t.Fatalf("excluded factory %q is registered", excludedFactory)
		}
	}
}

func TestConnectionFactoryUsesStrictTypedConfig(t *testing.T) {
	t.Parallel()
	registry, err := NewCatalog(Environment{WorkingDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		label       string
		factoryName string
		input       string
		wantMessage string
	}{
		{label: "unknown", factoryName: ConnectionFactoryName, input: `{"listenAddress":"127.0.0.1:0","extra":true}`, wantMessage: "unknown field"},
		{label: "wrong type", factoryName: ConnectionFactoryName, input: `{"listenAddress":7}`, wantMessage: "cannot unmarshal"},
		{label: "negative limit", factoryName: ConnectionFactoryName, input: `{"listenAddress":"127.0.0.1:0","maxBodyBytes":-1}`, wantMessage: "must not be negative"},
		{label: "empty version", factoryName: APIProxyFactoryName, input: `{"version":""}`, wantMessage: "version must be non-empty"},
		{label: "prompt unknown", factoryName: SystemPromptFactoryName, input: `{"unknown":true}`, wantMessage: "unknown field"},
		{label: "prompt wrong type", factoryName: SystemPromptFactoryName, input: `{"includeHarnessIdentity":"yes"}`, wantMessage: "must be a boolean"},
		{label: "prompt null bool", factoryName: SystemPromptFactoryName, input: `{"includeHarnessIdentity":null}`, wantMessage: "must be a boolean"},
		{label: "prompt null order", factoryName: SystemPromptFactoryName, input: `{"toolOrder":null}`, wantMessage: "must be an array"},
		{label: "prompt order", factoryName: SystemPromptFactoryName, input: `{"toolOrder":["bash"]}`, wantMessage: "must contain"},
		{label: "tools unknown", factoryName: ToolsFactoryName, input: `{"unknown":true}`, wantMessage: "unknown field"},
		{label: "tools null mode", factoryName: ToolsFactoryName, input: `{"mode":null}`, wantMessage: "mode must be"},
		{label: "tools code mode", factoryName: ToolsFactoryName, input: `{"mode":"code"}`, wantMessage: "Code Runtime bridge"},
		{label: "tools parallel limit", factoryName: ToolsFactoryName, input: `{"maxParallelSubCalls":0}`, wantMessage: "positive integer"},
		{label: "dynamic", factoryName: ConnectionFactoryName, input: `!!js (() => ({ listenAddress: "127.0.0.1:0" }))`, wantMessage: "invalid config"},
	} {
		testCase := testCase
		t.Run(testCase.label, func(t *testing.T) {
			t.Parallel()
			if _, err := registry.Create(context.Background(), testCase.factoryName, json.RawMessage(testCase.input)); err == nil || !strings.Contains(err.Error(), testCase.wantMessage) {
				t.Fatalf("Create error = %v, want containing %q", err, testCase.wantMessage)
			}
		})
	}
}

func TestConnectionCompositionSettlesDependenciesAndServesHostDescribe(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	registry, err := NewCatalog(Environment{WorkingDirectory: "/contract-workspace"})
	if err != nil {
		t.Fatal(err)
	}
	declarations, err := DefaultSpecs("127.0.0.1:0", "0.1.0-rc.5")
	if err != nil {
		t.Fatal(err)
	}
	engine := plugin.NewRuntime()
	if _, err := Load(requestContext, engine, registry, declarations); err != nil {
		t.Fatal(err)
	}
	serverAddress := ""
	probe := probePlugin{body: func(_ context.Context, pluginScope *plugin.Scope) error {
		serverEndpoint, found := plugin.Require(pluginScope, serverServiceKey)
		if !found {
			t.Fatal("webServer service is unavailable")
		}
		serverAddress = serverEndpoint.Address()
		sessionStore, found := plugin.Require(pluginScope, session.StoreService)
		if !found {
			t.Fatal("sessions service is unavailable")
		}
		if _, createErr := sessionStore.Create(requestContext, pluginScope, nil, session.CreateOptions{}); createErr != nil {
			return createErr
		}
		promptService, found := plugin.Require(pluginScope, systemprompt.Service)
		if !found {
			t.Fatal("systemPrompt service is unavailable")
		}
		assembled, assembleErr := promptService.Assemble(requestContext, systemprompt.AssembleContext{})
		if assembleErr != nil {
			return assembleErr
		}
		promptText, renderErr := systemprompt.RenderPrompt(assembled)
		if renderErr != nil {
			return renderErr
		}
		if promptText != "You are an AI agent powered by DeepSeek Harness." {
			t.Fatalf("default system prompt = %q", promptText)
		}
		toolService, found := plugin.Require(pluginScope, toolscore.Service)
		if !found {
			t.Fatal("tools service is unavailable")
		}
		if projections := toolService.Schemas(plugin.ScopeKey{}); len(projections) != 0 {
			t.Fatalf("default tool schemas = %#v", projections)
		}
		return nil
	}}
	if _, err := engine.Load(requestContext, probe); err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"type":"client-request","rpcId":"assembly-1","method":"host.describe","payload":{}}`)
	httpRequest, err := http.NewRequestWithContext(requestContext, http.MethodPost,
		"http://"+serverAddress+protocol.APIPath+"/"+apiproxy.HostDescribeMethod, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	httpRequest.Header.Set("content-type", "application/json")
	httpClient := &http.Client{Timeout: 2 * time.Second}
	response, err := httpClient.Do(httpRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var message protocol.ServerResponse
	if err := json.NewDecoder(response.Body).Decode(&message); err != nil {
		t.Fatal(err)
	}
	var description apiproxy.HostDescription
	if err := json.Unmarshal(message.Result.Value, &description); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !message.Result.OK || description.Version != "0.1.0-rc.5" ||
		description.CWD != "/contract-workspace" || description.AttachedSessions != 1 {
		t.Fatalf("response = (%d, %#v, %#v)", response.StatusCode, message, description)
	}
	if err := engine.Shutdown(requestContext); err != nil {
		t.Fatal(err)
	}
	for _, status := range engine.Statuses() {
		if status.State != plugin.StateStopped || len(status.Effects) != 0 {
			t.Fatalf("shutdown status = %#v", status)
		}
	}
}

func TestCompositionFailureRollsBackEarlierDeclarations(t *testing.T) {
	t.Parallel()
	reservedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer reservedListener.Close()
	registry, err := NewCatalog(Environment{WorkingDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	declarations, err := DefaultSpecs(reservedListener.Addr().String(), "test")
	if err != nil {
		t.Fatal(err)
	}
	engine := plugin.NewRuntime()
	if _, err := Load(context.Background(), engine, registry, declarations); err == nil {
		t.Fatal("composition with occupied listener succeeded")
	}
	if statuses := engine.Statuses(); len(statuses) != 0 {
		t.Fatalf("rolled-back statuses = %#v", statuses)
	}
}
