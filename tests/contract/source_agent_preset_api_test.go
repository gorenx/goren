//go:build contract

package contract_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gorenx/goren/apiproxy"
	connectionhost "github.com/gorenx/goren/internal/connection"
)

func TestPinnedSourceAgentPresetWebApiClientAcceptsAbsentGoRoster(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	methods := apiproxy.NewCatalog()
	gateway := apiproxy.NewAgentPresetGateway(nil, apiproxy.AgentPresetGatewayOptions{})
	if err := apiproxy.RegisterAgentPresetListAPI(methods, gateway); err != nil {
		t.Fatal(err)
	}
	idleMux := func(requestContext context.Context, _ func(apiproxy.StreamRequest[apiproxy.MuxFrame]) error) error {
		<-requestContext.Done()
		return nil
	}
	idleHost := func(requestContext context.Context, _ func(apiproxy.StreamRequest[apiproxy.HostFrame]) error) error {
		<-requestContext.Done()
		return nil
	}
	streams, err := apiproxy.NewEventStreams(idleMux, idleHost)
	if err != nil {
		t.Fatal(err)
	}
	httpHost, err := connectionhost.NewHTTPHost(connectionhost.HTTPConfig{}, methods, streams)
	if err != nil {
		t.Fatal(err)
	}
	testServer := httptest.NewServer(httpHost)
	defer testServer.Close()
	defer func() {
		closeContext, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelClose()
		if err := httpHost.Close(closeContext); err != nil {
			t.Errorf("close Go host: %v", err)
		}
	}()

	commandContext, cancelCommand := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelCommand()
	output, err := runTypeScript(commandContext, sourceRoot,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "agent-preset-client.ts"),
		sourceRoot, testServer.URL,
	)
	if err != nil {
		t.Fatal(err)
	}
	var observation apiproxy.AgentPresetListValue
	if err := json.Unmarshal(output, &observation); err != nil {
		t.Fatalf("decode source Agent Preset client observation: %v; output = %s", err, output)
	}
	want := apiproxy.AgentPresetListValue{Presets: []apiproxy.AgentPresetEntry{}}
	if !reflect.DeepEqual(observation, want) {
		t.Fatalf("list = %#v, want %#v", observation, want)
	}
}
