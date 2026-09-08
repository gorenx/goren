package apiproxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/apiproxy"
	commandapi "github.com/gorenx/goren/apiproxy/commands"
	credentialsapi "github.com/gorenx/goren/apiproxy/credentials"
	sessionapi "github.com/gorenx/goren/apiproxy/session"
	sessionevents "github.com/gorenx/goren/apiproxy/session/events"
	"github.com/gorenx/goren/apiproxy/stream"
	workspaceapi "github.com/gorenx/goren/apiproxy/workspace"
	"github.com/gorenx/goren/connection"
	carrier "github.com/gorenx/goren/internal/connection"
	"github.com/gorenx/goren/session"
)

// Unused ports deliberately panic if these transport-only tests enter domain work.
// Real domain clients and lifetime behavior have separate Registry integration tests.
type serverAgents struct{ sessionapi.Agents }
type serverSessions struct{ sessionapi.LiveSessions }
type serverHistory struct{ sessionapi.History }
type serverModels struct{ sessionapi.Models }
type serverTitles struct{ sessionapi.Titles }
type serverWorkspaces struct{ sessionapi.Workspaces }
type serverDirectory struct{ workspaceapi.Directory }
type serverCommands struct{ commandapi.Resolver }
type serverCredentials struct{ credentialsapi.Provider }
type serverSearch struct{ sessionapi.Search }
type serverSnapshots struct{ sessionevents.Snapshots }

func (serverSnapshots) ListSnapshots(context.Context) ([]sessionevents.Snapshot, error) {
	return []sessionevents.Snapshot{{
		CreationID: "current",
		Header: session.Header{
			ID: "conversation",
		},
	}}, nil
}

func configuredServer(t *testing.T) *apiproxy.Server {
	t.Helper()
	endpoint, err := apiproxy.NewServer(context.Background(), apiproxy.Dependencies{
		Sessions: sessionapi.Dependencies{
			Agents:      serverAgents{},
			Sessions:    serverSessions{},
			Persistence: serverHistory{},
			Models:      serverModels{},
			Titles:      serverTitles{},
			Workspaces:  serverWorkspaces{},
			Directories: sessionapi.DirectoryProvisionerFunc(func(string) error { t.Fatal("unexpected directory effect"); return nil }),
		},
		Search:      serverSearch{},
		Snapshots:   serverSnapshots{},
		Workspaces:  serverDirectory{},
		Commands:    serverCommands{},
		Credentials: serverCredentials{},
		Description: apiproxy.HostDescriptionFunc(func(context.Context) (apiproxy.HostDescription, error) {
			return apiproxy.HostDescription{
				Version: "test",
				CWD:     "/project",
			}, nil
		}),
	}, apiproxy.Options{
		Sessions: sessionapi.Options{
			WorkingDirectory: "/project",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := endpoint.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return endpoint
}

func TestServerRegistersDomainsAndDispatchesThroughHTTP(t *testing.T) {
	endpoint := configuredServer(t)
	for _, method := range []string{
		"host.describe", "session.create", "session.list", "session.search", "session.prompt",
		"session.cancel", "session.rename", "workspace.list", "llm.providers",
		"commands/list", "credentials.describe", "agentPreset.list", "settings.describe",
	} {
		if !endpoint.HasUnary(method) {
			t.Errorf("missing method %q", method)
		}
	}
	host, err := carrier.NewHTTPHost(carrier.HTTPConfig{}, endpoint, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://localhost/api/host.describe", strings.NewReader(`{"type":"client-request","rpcId":"request-1","method":"host.describe","payload":{}}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	host.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
	}
	var envelope struct {
		RPCID  string               `json:"rpcId"`
		Result connection.RPCResult `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.RPCID != "request-1" || !envelope.Result.OK {
		t.Fatalf("response: %s", response.Body.String())
	}
}

func TestServerCloseEndsMuxAndRejectsRequests(t *testing.T) {
	endpoint := configuredServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	baseline := make(chan struct{}, 1)
	finished := make(chan error, 1)
	go func() {
		finished <- endpoint.Mux(ctx, func(connection.RPCRequest) error { baseline <- struct{}{}; return nil })
	}()
	select {
	case <-baseline:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := endpoint.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if endpoint.HasUnary(apiproxy.HostDescribeMethod) {
		t.Fatal("closed server admitted method")
	}
	_, err := endpoint.DispatchUnary(ctx, apiproxy.HostDescribeMethod, "late", json.RawMessage(`{}`))
	if !errors.Is(err, stream.ErrClosed) {
		t.Fatalf("late dispatch: %v", err)
	}
	if err := endpoint.Host(ctx, nil); !errors.Is(err, stream.ErrClosed) {
		t.Fatalf("late Host: %v", err)
	}
	if err := endpoint.Mux(ctx, nil); !errors.Is(err, stream.ErrClosed) {
		t.Fatalf("late Mux: %v", err)
	}
	if err := endpoint.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
