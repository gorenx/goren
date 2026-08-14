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
)

type probePlugin struct {
	body func(context.Context, *plugin.Scope) error
}

func (instance probePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: "assembly-probe", Requires: []plugin.ServiceRef{serverServiceKey.Ref()}}
}

func (instance probePlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	return instance.body(requestContext, pluginScope)
}

func TestCatalogContainsOnlyCurrentConnectionSlice(t *testing.T) {
	t.Parallel()
	registry, err := NewCatalog(Environment{WorkingDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{ConnectionFactoryName, APIProxyFactoryName}
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
	if response.StatusCode != http.StatusOK || !message.Result.OK || description.Version != "0.1.0-rc.5" || description.CWD != "/contract-workspace" {
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
