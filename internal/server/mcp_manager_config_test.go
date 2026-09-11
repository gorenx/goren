package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/mcpclient"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	registryactor "github.com/gorenx/goren/tools/registry/actor"
	"github.com/gorenx/goren/tools/tool"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

func startMCPManager(t *testing.T) *mcpManager {
	t.Helper()
	system, err := goakt.NewActorSystem("mcp-settings-test", goakt.WithPubSub(), goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	directory, err := goakt.GrainOf[*registryactor.RegistryGrain](t.Context(), system, registryactor.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	owner := newMCPManager(system, directory, time.Second, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := owner.close(ctx); err != nil {
			t.Error(err)
		}
		if err := system.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return owner
}

func mcpHTTPConfig(name, url string) mcpclient.Config {
	return mcpclient.Config{
		ServerName: name,
		Transport:  "streamable-http",
		URL:        url,
	}
}

func managerProvider(t *testing.T, owner *mcpManager, name string) tool.IdentifiedProvider {
	t.Helper()
	owner.mu.RLock()
	entry, found := owner.entries[name]
	owner.mu.RUnlock()
	if !found {
		t.Fatalf("missing connection for %s", name)
	}
	source, exists := owner.Provider(mcpclient.ToolName(name, "read"), entry.connection.ProviderID())
	identified, valid := source.(tool.IdentifiedProvider)
	if !exists || !valid {
		t.Fatalf("missing Provider for %s", name)
	}
	return identified
}

func awaitMCPCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("MCP condition did not settle")
		}
	}
}

func TestMCPManagerReconcileReusesConnectionsAndRetriesPartialFailure(t *testing.T) {
	first := &mcpSource{}
	second := &mcpSource{}
	second.unavailable.Store(true)
	firstHTTP := httptest.NewServer(first)
	t.Cleanup(firstHTTP.Close)
	secondHTTP := httptest.NewServer(second)
	t.Cleanup(secondHTTP.Close)
	owner := startMCPManager(t)
	definitions := []mcpclient.Config{
		mcpHTTPConfig("first", firstHTTP.URL),
		mcpHTTPConfig("second", secondHTTP.URL),
	}
	if err := owner.Reconcile(t.Context(), definitions); err == nil {
		t.Fatal("partial target was reported fully applied")
	}
	original := managerProvider(t, owner, "first").ProviderID()
	second.unavailable.Store(false)
	if err := owner.Reconcile(t.Context(), definitions); err != nil {
		t.Fatal(err)
	}
	if managerProvider(t, owner, "first").ProviderID() != original || first.listens.Load() != 1 {
		t.Fatal("retry reconnected the already applied target")
	}
	if err := owner.Reconcile(t.Context(), definitions); err != nil {
		t.Fatal(err)
	}
	if first.listens.Load() != 1 || second.listens.Load() != 1 {
		t.Fatal("identical target reopened connections")
	}
	// A failed replacement preserves the healthy connection at the same name.
	second.unavailable.Store(true)
	if err := owner.Reconcile(t.Context(), []mcpclient.Config{mcpHTTPConfig("first", secondHTTP.URL)}); err == nil {
		t.Fatal("unavailable replacement succeeded")
	}
	if managerProvider(t, owner, "first").ProviderID() != original || first.closed.Load() != 0 {
		t.Fatal("failed candidate destroyed the old connection")
	}
	if len(owner.listConnections()) != 1 {
		t.Fatal("partial target did not remove an unrelated obsolete connection")
	}
	second.unavailable.Store(false)
	if err := owner.Reconcile(t.Context(), []mcpclient.Config{mcpHTTPConfig("first", secondHTTP.URL)}); err != nil {
		t.Fatal(err)
	}
	if managerProvider(t, owner, "first").ProviderID() == original {
		t.Fatal("same-schema replacement retained the original Provider")
	}
	awaitMCPCondition(t, func() bool { return first.closed.Load() == 1 })
	if err := owner.Reconcile(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if len(owner.listConnections()) != 0 {
		t.Fatal("empty HTTP target retained a connection")
	}
}

func TestMCPManagerReconcileDrainsCallsAndRestoresCanceledReplacement(t *testing.T) {
	for _, mode := range []string{"replace", "cancel", "remove", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			release := make(chan struct{})
			first := &mcpSource{
				entered: make(chan struct{}, 2),
				release: release,
			}
			second := &mcpSource{}
			firstHTTP := httptest.NewServer(first)
			t.Cleanup(firstHTTP.Close)
			secondHTTP := httptest.NewServer(second)
			t.Cleanup(secondHTTP.Close)
			owner := startMCPManager(t)
			identity, err := goakt.GrainOf[*mcpGrain](ctx, owner.system, "mcp.management", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			payload, err := owner.system.AskGrain(ctx, identity, bindMCP{
				manager: owner,
			}, time.Second)
			if err != nil || payload.(mcpReply).err != nil {
				t.Fatalf("MCP binding=%v %v", payload, err)
			}
			endpoint := &mcpDirectory{
				system:   owner.system,
				identity: identity,
			}
			if err := owner.Reconcile(ctx, []mcpclient.Config{mcpHTTPConfig("fixture", firstHTTP.URL)}); err != nil {
				t.Fatal(err)
			}
			source := managerProvider(t, owner, "fixture")
			body, err := source.Open(ctx)
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan tools.BodyResult, 1)
			go func() {
				result <- (tools.Invocation{
					Name:      mcpclient.ToolName("fixture", "read"),
					SessionID: "agent",
					CallID:    "call",
					Arguments: json.RawMessage(`{}`),
				}).Run(ctx, body.Definition.Executor)
			}()
			select {
			case <-first.entered:
			case <-ctx.Done():
				t.Fatal("tool body did not enter")
			}
			target := []mcpclient.Config{mcpHTTPConfig("fixture", secondHTTP.URL)}
			if mode == "remove" {
				target = nil
			}
			waiting, stop := context.WithCancel(ctx)
			defer stop()
			applied := make(chan error, 1)
			go func() { applied <- endpoint.Reconcile(waiting, target) }()
			awaitMCPCondition(t, func() bool {
				_, err := source.Open(ctx)
				return errors.Is(err, mcpclient.ErrDraining)
			})
			select {
			case err := <-applied:
				t.Fatalf("configuration settled before body exit: %v", err)
			default:
			}
			if first.canceled.Load() != 0 || first.closed.Load() != 0 {
				t.Fatal("configuration canceled active work")
			}
			query, finishQuery := context.WithTimeout(ctx, 250*time.Millisecond)
			views, err := endpoint.ListConnections(query)
			finishQuery()
			if err != nil || len(views) != 1 {
				t.Fatalf("MCP status blocked behind connection drain: %v", err)
			}
			if mode == "shutdown" {
				if err := owner.close(ctx); err != nil {
					t.Fatal(err)
				}
				if err := <-applied; !errors.Is(err, context.Canceled) {
					t.Fatalf("shutdown did not cancel waiting update: %v", err)
				}
				if outcome := <-result; outcome.Err == nil {
					t.Fatal("shutdown did not cancel tool body")
				}
				return
			}
			if mode == "cancel" {
				stop()
				if err := <-applied; !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation=%v", err)
				}
				// AskGrain cancellation ends the caller's wait before the managed
				// task confirms rollback. Observe actual restored admission.
				awaitMCPCondition(t, func() bool {
					_, err := source.Open(ctx)
					return err == nil
				})
				if managerProvider(t, owner, "fixture").ProviderID() != source.ProviderID() {
					t.Fatal("canceled candidate became current")
				}
				awaitMCPCondition(t, func() bool { return second.closed.Load() == 1 })
			}
			close(release)
			if outcome := <-result; outcome.Err != nil {
				t.Fatalf("original call was interrupted: %v", outcome.Err)
			}
			if mode == "cancel" {
				if err := owner.Reconcile(ctx, target); err != nil {
					t.Fatal(err)
				}
			} else if err := <-applied; err != nil {
				t.Fatal(err)
			}
			if mode == "remove" {
				if len(owner.listConnections()) != 0 {
					t.Fatal("drained connection was not removed")
				}
			} else if managerProvider(t, owner, "fixture").ProviderID() == source.ProviderID() {
				t.Fatal("drained connection was not replaced")
			}
			awaitMCPCondition(t, func() bool { return first.closed.Load() == 1 })
		})
	}
}

func TestMCPManagerRestoresCatalogAndProviderAfterPublicationFailure(t *testing.T) {
	first := &mcpSource{}
	second := &mcpSource{
		toolName: "conflict",
	}
	firstHTTP := httptest.NewServer(first)
	t.Cleanup(firstHTTP.Close)
	secondHTTP := httptest.NewServer(second)
	t.Cleanup(secondHTTP.Close)
	owner := startMCPManager(t)
	ctx := t.Context()
	if err := owner.Reconcile(ctx, []mcpclient.Config{mcpHTTPConfig("fixture", firstHTTP.URL)}); err != nil {
		t.Fatal(err)
	}
	original := managerProvider(t, owner, "fixture")
	payload, err := owner.system.AskGrain(ctx, owner.directory, registryactor.RegisterSource{
		Name: "conflicting-source",
		Catalog: toolregistry.Catalog{
			Schemas: []llm.ToolSchema{
				{
					Name:       mcpclient.ToolName("fixture", "conflict"),
					Parameters: json.RawMessage(`{"type":"object"}`),
				},
			},
		},
	}, time.Second)
	if err != nil || payload.(registryactor.Completion).Err != nil {
		t.Fatalf("conflict setup=%v %v", payload, err)
	}
	if err := owner.Reconcile(ctx, []mcpclient.Config{mcpHTTPConfig("fixture", secondHTTP.URL)}); err == nil {
		t.Fatal("conflicting catalog was published")
	}
	if managerProvider(t, owner, "fixture").ProviderID() != original.ProviderID() {
		t.Fatal("failed publication replaced Provider")
	}
	if _, err := original.Open(ctx); err != nil {
		t.Fatalf("rollback did not restore admission: %v", err)
	}
	payload, err = owner.system.AskGrain(ctx, owner.directory, registryactor.ReadConfiguration{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if payload.(registryactor.Configuration).Value.Global.ProviderIDs[mcpclient.ToolName("fixture", "read")] != original.ProviderID() {
		t.Fatal("rollback did not restore catalog identity")
	}
	awaitMCPCondition(t, func() bool { return second.closed.Load() == 1 })
}

func TestMCPManagerHTTPConfigurationPreservesStdioAndValidatesBeforeEffects(t *testing.T) {
	t.Setenv("GOREN_MCP_MANAGER_STDIO_FIXTURE", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	remote := &mcpSource{}
	httpEndpoint := httptest.NewServer(remote)
	t.Cleanup(httpEndpoint.Close)
	owner := startMCPManager(t)
	if _, err := owner.connect(t.Context(), mcpclient.Config{
		ServerName: "local",
		Transport:  "stdio",
		Command:    executable,
		Args:       []string{"-test.run=^TestMCPManagerStdioFixtureProcess$"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]mcpclient.Config{
		{mcpHTTPConfig("remote", httpEndpoint.URL), mcpHTTPConfig("local", httpEndpoint.URL)},
		{mcpHTTPConfig("remote", httpEndpoint.URL), mcpHTTPConfig("remote", httpEndpoint.URL)},
		{
			{
				ServerName: "remote",
				Transport:  "stdio",
				Command:    executable,
			},
		},
	} {
		if err := owner.Validate(t.Context(), invalid); err == nil {
			t.Fatal("pre-save validation admitted an invalid HTTP target")
		}
		if err := owner.Reconcile(t.Context(), invalid); err == nil {
			t.Fatal("invalid HTTP target was admitted")
		}
	}
	if remote.listens.Load() != 0 {
		t.Fatal("validation failure acquired a network connection")
	}
	if err := owner.Reconcile(t.Context(), []mcpclient.Config{mcpHTTPConfig("remote", httpEndpoint.URL)}); err != nil {
		t.Fatal(err)
	}
	if err := owner.Reconcile(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	views := owner.listConnections()
	if len(views) != 1 ||
		views[0].ServerName != "local" ||
		views[0].Status != "connected" {
		t.Fatalf("HTTP reset changed local stdio: %+v", views)
	}
}

func TestMCPManagerStdioFixtureProcess(t *testing.T) {
	if os.Getenv("GOREN_MCP_MANAGER_STDIO_FIXTURE") != "1" {
		return
	}
	reader := bufio.NewScanner(os.Stdin)
	for reader.Scan() {
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(reader.Bytes(), &message) != nil {
			os.Exit(2)
		}
		switch message.Method {
		case "server/discover":
			fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"resultType\":\"complete\",\"supportedVersions\":[\"2026-07-28\"],\"capabilities\":{\"tools\":{\"listChanged\":true}}}}\n", message.ID)
		case "tools/list":
			fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"resultType\":\"complete\",\"tools\":[]}}\n", message.ID)
		case "subscriptions/listen":
			fmt.Println(`{"jsonrpc":"2.0","method":"notifications/subscriptions/acknowledged","params":{"notifications":{"toolsListChanged":true},"_meta":{"io.modelcontextprotocol/subscriptionId":"stdio-sub"}}}`)
		}
	}
	os.Exit(0)
}
