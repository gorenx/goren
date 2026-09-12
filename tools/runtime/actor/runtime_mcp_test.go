package runtimeactor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/mcpclient"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	"github.com/gorenx/goren/toolaskuser"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	"github.com/gorenx/goren/tools/tool"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// mcpServer implements the wire boundary only. Runtime, ToolActors, Provider
// resolution and the complete tool-call protocol remain production code.
type mcpServer struct {
	result         string
	names          atomic.Pointer[[]string]
	notices        chan struct{}
	disconnect     chan struct{}
	listens        atomic.Int32
	closed         atomic.Int32
	calls          atomic.Int32
	unavailable    atomic.Bool
	pauseDiscovery atomic.Bool
	discovering    chan struct{}
	resume         chan struct{}
}

func (remote *mcpServer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if remote.unavailable.Load() {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if json.NewDecoder(request.Body).Decode(&envelope) != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	respond := func(value string) {
		fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":%s}`, envelope.ID, value)
	}
	switch envelope.Method {
	case "server/discover":
		if remote.pauseDiscovery.Load() {
			select {
			case remote.discovering <- struct{}{}:
			default:
			}
			select {
			case <-remote.resume:
			case <-request.Context().Done():
				return
			}
		}
		respond(`{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{"listChanged":true}}}`)
	case "tools/list":
		definitions := make([]json.RawMessage, 0)
		for _, name := range *remote.names.Load() {
			definitions = append(definitions, json.RawMessage(fmt.Sprintf(`{"name":%q,"inputSchema":{"type":"object"}}`, name)))
		}
		encoded, _ := json.Marshal(definitions)
		respond(fmt.Sprintf(`{"resultType":"complete","tools":%s}`, encoded))
	case "subscriptions/listen":
		remote.listens.Add(1)
		defer remote.closed.Add(1)
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/subscriptions/acknowledged\",\"params\":{\"notifications\":{\"toolsListChanged\":true},\"_meta\":{\"io.modelcontextprotocol/subscriptionId\":\"sub\"}}}\n\n")
		writer.(http.Flusher).Flush()
		for {
			select {
			case <-remote.notices:
				fmt.Fprint(writer, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n")
				writer.(http.Flusher).Flush()
			case <-remote.disconnect:
				return
			case <-request.Context().Done():
				return
			}
		}
	case "tools/call":
		remote.calls.Add(1)
		respond(fmt.Sprintf(`{"resultType":"complete","content":[{"type":"text","text":%q}]}`, remote.result))
	default:
		writer.WriteHeader(http.StatusBadRequest)
	}
}

func serveMCP(t *testing.T, result string) (*mcpServer, mcpclient.Config) {
	t.Helper()
	remote := &mcpServer{
		result:      result,
		notices:     make(chan struct{}, 1),
		disconnect:  make(chan struct{}),
		discovering: make(chan struct{}, 1),
		resume:      make(chan struct{}),
	}
	names := []string{"read"}
	remote.names.Store(&names)
	endpoint := httptest.NewServer(remote)
	t.Cleanup(func() {
		endpoint.CloseClientConnections()
		endpoint.Close()
	})
	return remote, mcpclient.Config{
		ServerName:       "fixture",
		Transport:        "streamable-http",
		URL:              endpoint.URL,
		RequestTimeoutMs: 500,
	}
}

func newMCPRuntime(t *testing.T, system goakt.ActorSystem, registry *goakt.GrainIdentity, identifier session.SessionID, selection *toolregistry.Catalog, definitions []mcpclient.Config) (*runtimeactor.Client, *goakt.GrainIdentity) {
	t.Helper()
	authority, err := goakt.GrainOf[*runtimeAuthority](t.Context(), system, string(identifier)+".authority", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	if selection != nil {
		captured := selection.Clone()
		captured.MCP = definitions
		selection = &captured
	}
	sender, err := runtimeactor.NewClient(t.Context(), system, runtimeactor.InitializeRuntime{
		AgentID:    identifier,
		CreationID: "creation",
		Authority:  authority,
		Registry:   registry,
		Config:     selection,
		Timeout:    time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := closeClient(t, system, sender, ctx, ask[string](t, system, authority, readClaim{})); err != nil {
			t.Error(err)
		}
	})
	return sender, authority
}

func invokeMCP(t *testing.T, system goakt.ActorSystem, address *goakt.GrainIdentity, identifier session.SessionID, name, expected string) {
	t.Helper()
	prepared := ask[runtimeactor.CallResult](t, system, address, runtimeactor.PrepareCall{
		CreationID: "creation",
		Context:    t.Context(),
		Input: tools.ToolExecutionInput{
			SessionID: identifier,
			CallID:    "read-call",
			Name:      name,
			Arguments: json.RawMessage(`{}`),
		},
	})
	if prepared.Err != nil || prepared.Stage != tools.ScheduledDispatch {
		t.Fatalf("preparation: %+v", prepared)
	}
	finishMCP(t, system, address, expected)
}

func finishMCP(t *testing.T, system goakt.ActorSystem, address *goakt.GrainIdentity, expected string) {
	t.Helper()
	dispatched := ask[runtimeactor.CallResult](t, system, address, runtimeactor.DispatchCall{
		CreationID: "creation",
		CallID:     "read-call",
	})
	if dispatched.Err != nil {
		t.Fatal(dispatched.Err)
	}
	final := ask[runtimeactor.CallResult](t, system, address, runtimeactor.FinalizeCall{
		CreationID: "creation",
		CallID:     "read-call",
	})
	if final.Err != nil || final.Result == nil || final.Result.Failed() {
		t.Fatalf("MCP result: %+v", final)
	}
	value, ok := final.Result.SuccessValue()
	if !ok || !bytes.Contains(value, []byte(expected)) {
		t.Fatalf("wrong endpoint result: %s", value)
	}
	released := ask[runtimeactor.Completion](t, system, address, runtimeactor.ReleaseCall{
		CreationID: "creation",
		CallID:     "read-call",
	})
	if released.Err != nil {
		t.Fatal(released.Err)
	}
}

func TestRuntimeMCPReplacesConfigurationAfterCallsFinish(t *testing.T) {
	firstRemote, firstConfig := serveMCP(t, "first endpoint")
	secondRemote, secondConfig := serveMCP(t, "second endpoint")
	system, registry, catalog := startCatalog(t, true, nil)
	sender, _ := newMCPRuntime(t, system, registry, "agent", &toolregistry.Catalog{}, []mcpclient.Config{firstConfig})
	if err := startClient(t, system, sender, t.Context()); err != nil {
		t.Fatal(err)
	}
	name := mcpclient.ToolName("fixture", "read")
	before := ask[runtimeactor.Definitions](t, system, sender.Runtime(), runtimeactor.ReadDefinitions{
		CreationID: "creation",
	})
	if before.Err != nil || len(before.Value.Schemas) != 1 {
		t.Fatalf("initial definitions: %+v", before)
	}
	original := ask[catalogactor.ToolAddress](t, system, registry, catalogactor.FindTool{
		AgentID: "agent",
		Name:    name,
	})
	prepared := ask[runtimeactor.CallResult](t, system, sender.Runtime(), runtimeactor.PrepareCall{
		CreationID: "creation",
		Context:    t.Context(),
		Input: tools.ToolExecutionInput{
			SessionID: "agent",
			CallID:    "read-call",
			Name:      name,
			Arguments: json.RawMessage(`{}`),
		},
	})
	if prepared.Err != nil || prepared.Stage != tools.ScheduledDispatch {
		t.Fatalf("preparation: %+v", prepared)
	}
	if err := catalog.ConfigureSession(t.Context(), "agent", toolregistry.Catalog{
		MCP: []mcpclient.Config{secondConfig},
	}); err != nil {
		t.Fatal(err)
	}
	refresh := func() runtimeactor.Definitions {
		return ask[runtimeactor.Definitions](t, system, sender.Runtime(), runtimeactor.RefreshConfiguration{
			CreationID: "creation",
			ID:         "configuration",
		})
	}
	if result := refresh(); result.Err == nil {
		t.Fatal("configuration changed with a prepared call still active")
	}
	if firstRemote.closed.Load() != 0 || secondRemote.listens.Load() != 0 {
		t.Fatal("active call did not retain the original connection")
	}
	finishMCP(t, system, sender.Runtime(), firstRemote.result)

	// Failed discovery must leave the applied tools usable on the first endpoint.
	secondRemote.unavailable.Store(true)
	failed := awaitProviderRefresh(t, refresh, func(result runtimeactor.Definitions) bool {
		return result.Err != nil
	})
	if failed.Err == nil || firstRemote.closed.Load() != 0 {
		t.Fatal("failed replacement released the applied connection")
	}
	invokeMCP(t, system, sender.Runtime(), "agent", name, firstRemote.result)
	secondRemote.unavailable.Store(false)
	replaced := refresh()
	if replaced.Err != nil || len(replaced.Value.Schemas) != 1 {
		t.Fatalf("replacement retry: %+v", replaced)
	}
	awaitMCPClosed(t, firstRemote)
	if secondRemote.listens.Load() != 1 || secondRemote.closed.Load() != 0 {
		t.Fatal("replacement did not retain exactly one live candidate")
	}
	if replaced.Value.Schemas[0].Version != before.Value.Schemas[0].Version {
		t.Fatal("changing only the endpoint changed the model schema version")
	}
	current := ask[catalogactor.ToolAddress](t, system, registry, catalogactor.FindTool{
		AgentID: "agent",
		Name:    name,
	})
	if !original.Found || !current.Found || original.CreationID != current.CreationID || !original.Actor.Equal(current.Actor) {
		t.Fatal("changing the endpoint recreated the ToolActor")
	}
	invokeMCP(t, system, sender.Runtime(), "agent", name, secondRemote.result)

	// A policy change requires application but must reuse the same connection.
	hidden := false
	if err := catalog.ConfigureSession(t.Context(), "agent", toolregistry.Catalog{
		MCP: []mcpclient.Config{secondConfig},
		Policies: map[string]toolregistry.Policy{
			name: {
				Visible: &hidden,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	reconfigured := awaitProviderRefresh(t, refresh, func(result runtimeactor.Definitions) bool {
		return result.Err != nil || len(result.Value.Schemas) == 0
	})
	if reconfigured.Err != nil || !slices.Equal(reconfigured.Value.KnownNames, []string{name}) || secondRemote.listens.Load() != 1 {
		t.Fatalf("policy application reopened or lost the connection: %+v", reconfigured)
	}
	invokeMCP(t, system, sender.Runtime(), "agent", name, secondRemote.result)

	if err := catalog.ConfigureSession(t.Context(), "agent", toolregistry.Catalog{}); err != nil {
		t.Fatal(err)
	}
	removed := awaitProviderRefresh(t, refresh, func(result runtimeactor.Definitions) bool {
		return result.Err != nil || len(result.Value.KnownNames) == 0
	})
	if removed.Err != nil {
		t.Fatal(removed.Err)
	}
	awaitMCPClosed(t, secondRemote)
	address := ask[catalogactor.ToolAddress](t, system, registry, catalogactor.FindTool{
		AgentID: "agent",
		Name:    name,
	})
	if address.Found {
		t.Fatal("removing the connection left an executable tool")
	}
}

func TestRuntimeMCPInitializationUsesOneConfiguration(t *testing.T) {
	remote, definition := serveMCP(t, "initial configuration")
	remote.pauseDiscovery.Store(true)
	system, registry, catalog := startCatalog(t, true, nil)
	sender, _ := newMCPRuntime(t, system, registry, "agent", &toolregistry.Catalog{}, []mcpclient.Config{definition})
	started := make(chan error, 1)
	go func() { started <- startClient(t, system, sender, t.Context()) }()
	select {
	case <-remote.discovering:
	case <-time.After(5 * time.Second):
		t.Fatal("MCP discovery did not start")
	}
	if err := catalog.ConfigureSession(t.Context(), "agent", toolregistry.Catalog{}); err != nil {
		t.Fatal(err)
	}
	close(remote.resume)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	current := ask[runtimeactor.RuntimeView](t, system, sender.Runtime(), runtimeactor.ReadRuntime{
		CreationID: "creation",
	})
	if current.Err != nil || len(current.Snapshot.Configuration.Global.MCP) != 1 || !slices.Contains(current.Snapshot.Names, mcpclient.ToolName("fixture", "read")) {
		t.Fatalf("initialization mixed Registry revisions: %+v", current)
	}
	updated := ask[runtimeactor.Definitions](t, system, sender.Runtime(), runtimeactor.RefreshConfiguration{
		CreationID: "creation",
		ID:         uuid.NewString(),
	})
	if updated.Err != nil || len(updated.Value.KnownNames) != 0 {
		t.Fatalf("next configuration did not apply independently: %+v", updated)
	}
}

func awaitMCPClosed(t *testing.T, remote *mcpServer) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for remote.closed.Load() != remote.listens.Load() {
		select {
		case <-deadline.C:
			t.Fatal("MCP subscription was not released")
		case <-tick.C:
		}
	}
}

func TestRuntimeMCPSessionsOwnConnectionsAndRefreshIndependently(t *testing.T) {
	firstRemote, firstConfig := serveMCP(t, "first result")
	secondRemote, secondConfig := serveMCP(t, "second result")
	system, registry, catalog := startCatalog(t, true, nil)
	definitions := []mcpclient.Config{firstConfig}
	first, firstAuthority := newMCPRuntime(t, system, registry, "first", &toolregistry.Catalog{}, definitions)
	definitions[0].URL = secondConfig.URL
	second, _ := newMCPRuntime(t, system, registry, "second", &toolregistry.Catalog{}, []mcpclient.Config{secondConfig})
	if firstRemote.listens.Load() != 0 || secondRemote.listens.Load() != 0 {
		t.Fatal("Client construction performed network I/O")
	}
	if err := startClient(t, system, first, t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := startClient(t, system, second, t.Context()); err != nil {
		t.Fatal(err)
	}
	name := mcpclient.ToolName("fixture", "read")
	firstCatalog, err := catalog.Read(t.Context(), "first")
	if err != nil {
		t.Fatal(err)
	}
	secondCatalog, err := catalog.Read(t.Context(), "second")
	if err != nil {
		t.Fatal(err)
	}
	if len(firstCatalog.Global.ProviderIDs) != 0 || len(secondCatalog.Global.ProviderIDs) != 0 {
		t.Fatal("Runtime connection identities leaked into Registry configuration")
	}
	invokeMCP(t, system, first.Runtime(), "first", name, firstRemote.result)
	original := ask[catalogactor.ToolAddress](t, system, registry, catalogactor.FindTool{
		AgentID: "first",
		Name:    name,
	})
	invokeMCP(t, system, second.Runtime(), "second", name, secondRemote.result)
	// Retain an explicit policy while the remote adds a tool.
	hidden := false
	firstCatalog.Global.Policies = map[string]toolregistry.Policy{
		name: {
			Visible: &hidden,
		},
	}
	for index := range firstCatalog.Global.Schemas {
		firstCatalog.Global.Schemas[index].Version = 0
	}
	if err := catalog.ConfigureSession(t.Context(), "first", firstCatalog.Global); err != nil {
		t.Fatal(err)
	}
	names := []string{"read", "search"}
	firstRemote.names.Store(&names)
	firstRemote.notices <- struct{}{}
	added := mcpclient.ToolName("fixture", "search")
	refreshed := awaitProviderRefresh(t, func() runtimeactor.Definitions {
		return ask[runtimeactor.Definitions](t, system, first.Runtime(), runtimeactor.RefreshConfiguration{
			CreationID: "creation",
			ID:         "remote-change",
		})
	}, func(result runtimeactor.Definitions) bool {
		return result.Err != nil || slices.Contains(result.Value.KnownNames, added)
	})
	if refreshed.Err != nil || len(refreshed.Value.Schemas) != 1 || refreshed.Value.Schemas[0].Name != added {
		t.Fatalf("refresh lost policy or new tool: %+v", refreshed)
	}
	unchanged := ask[runtimeactor.Definitions](t, system, second.Runtime(), runtimeactor.ReadDefinitions{
		CreationID: "creation",
	})
	if unchanged.Err != nil || len(unchanged.Value.Schemas) != 1 || unchanged.Value.Schemas[0].Name != name {
		t.Fatalf("another Session changed: %+v", unchanged)
	}
	invokeMCP(t, system, first.Runtime(), "first", added, firstRemote.result)
	invokeMCP(t, system, first.Runtime(), "first", name, firstRemote.result)
	retained := ask[catalogactor.ToolAddress](t, system, registry, catalogactor.FindTool{
		AgentID: "first",
		Name:    name,
	})
	if !original.Found || !retained.Found || original.CreationID != retained.CreationID || !original.Actor.Equal(retained.Actor) {
		t.Fatal("switching tools replaced the original ToolActor")
	}
	if firstRemote.listens.Load() != 1 || firstRemote.calls.Load() != 3 {
		t.Fatalf("read/search/read did not reuse one connection: listens=%d calls=%d", firstRemote.listens.Load(), firstRemote.calls.Load())
	}
	// Subscription termination withdraws only the failed Session's tool set.
	close(firstRemote.disconnect)
	withdrawn := awaitProviderRefresh(t, func() runtimeactor.Definitions {
		return ask[runtimeactor.Definitions](t, system, first.Runtime(), runtimeactor.RefreshConfiguration{
			CreationID: "creation",
			ID:         "remote-ended",
		})
	}, func(result runtimeactor.Definitions) bool {
		return result.Err == nil && len(result.Value.KnownNames) == 0
	})
	if withdrawn.Err != nil {
		t.Fatal(withdrawn.Err)
	}
	if err := closeClient(t, system, first, t.Context(), ask[string](t, system, firstAuthority, readClaim{})); err != nil {
		t.Fatal(err)
	}
	awaitMCPClosed(t, firstRemote)
	if secondRemote.closed.Load() != 0 {
		t.Fatal("closing one Runtime closed another connection")
	}
	invokeMCP(t, system, second.Runtime(), "second", name, secondRemote.result)
}

func TestRuntimeMCPInitializationFailureRetainsCleanup(t *testing.T) {
	for _, scenario := range []string{"invalid-config", "duplicate-server", "open-failure", "tool-conflict"} {
		t.Run(scenario, func(t *testing.T) {
			remote, configuration := serveMCP(t, "unused")
			system, registry, _ := startCatalog(t, true, nil)
			selection := toolregistry.Catalog{}
			definitions := []mcpclient.Config{configuration}
			expectedOpens := int32(0)
			switch scenario {
			case "invalid-config":
				definitions = append(definitions, mcpclient.Config{
					ServerName: "bad",
				})
			case "duplicate-server":
				definitions = append(definitions, configuration)
			case "open-failure":
				failed := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					writer.WriteHeader(http.StatusServiceUnavailable)
				}))
				t.Cleanup(failed.Close)
				definitions = append(definitions, mcpclient.Config{
					ServerName:       "bad",
					Transport:        "streamable-http",
					URL:              failed.URL,
					RequestTimeoutMs: 100,
				})
				expectedOpens = 1
			case "tool-conflict":
				selection.Schemas = []llm.ToolSchema{
					{
						Name:       mcpclient.ToolName("fixture", "read"),
						Parameters: json.RawMessage(`{"type":"object"}`),
					},
				}
				expectedOpens = 1
			}
			sender, authority := newMCPRuntime(t, system, registry, "agent", &selection, definitions)
			if err := startClient(t, system, sender, t.Context()); err == nil {
				t.Fatal("invalid MCP initialization succeeded")
			}
			if remote.listens.Load() != expectedOpens {
				t.Fatalf("opened %d connections, want %d", remote.listens.Load(), expectedOpens)
			}
			if err := closeClient(t, system, sender, t.Context(), ask[string](t, system, authority, readClaim{})); err != nil {
				t.Fatal(err)
			}
			awaitMCPClosed(t, remote)
			address := ask[catalogactor.ToolAddress](t, system, registry, catalogactor.FindTool{
				AgentID: "agent",
				Name:    mcpclient.ToolName("fixture", "read"),
			})
			if address.Found {
				t.Fatal("failed creation retained an executable tool")
			}
		})
	}
}

func TestRuntimeMCPClosesConnectionsOnlyAfterToolsRelease(t *testing.T) {
	remote, configuration := serveMCP(t, "unused")
	installed := &source{}
	system, registry, _ := startCatalog(t, true, tool.Providers{
		toolaskuser.Name: installed,
	})
	definition, err := toolaskuser.NewDefinition(questions{})
	if err != nil {
		t.Fatal(err)
	}
	sender, authority := newMCPRuntime(t, system, registry, "agent", &toolregistry.Catalog{
		Schemas: []llm.ToolSchema{
			{
				Name:        definition.Name,
				Description: definition.Description,
				Parameters:  definition.Parameters,
			},
		},
	}, []mcpclient.Config{configuration})
	if err := startClient(t, system, sender, t.Context()); err != nil {
		t.Fatal(err)
	}
	identifier := ask[string](t, system, authority, readClaim{})
	installed.closeFailure.Store(true)
	if err := closeClient(t, system, sender, t.Context(), identifier); err == nil {
		t.Fatal("failed tool release was acknowledged")
	}
	if remote.closed.Load() != 0 {
		t.Fatal("MCP connection closed before tools finished cleanup")
	}
	installed.closeFailure.Store(false)
	if err := closeClient(t, system, sender, t.Context(), identifier); err != nil {
		t.Fatal(err)
	}
	awaitMCPClosed(t, remote)
}

func TestRuntimeRecoveryInstallsMCPAddedAfterFailedToolUpdate(t *testing.T) {
	remote, configuration := serveMCP(t, "latest target")
	installed := &source{}
	system, registry, catalog := startCatalog(t, true, tool.Providers{
		toolaskuser.Name: installed,
	})
	definition, err := toolaskuser.NewDefinition(questions{})
	if err != nil {
		t.Fatal(err)
	}
	selection := toolregistry.Catalog{
		Schemas: []llm.ToolSchema{
			{
				Name:        definition.Name,
				Description: definition.Description,
				Parameters:  definition.Parameters,
			},
		},
	}
	sender, _ := newMCPRuntime(t, system, registry, "agent", &selection, nil)
	if err := startClient(t, system, sender, t.Context()); err != nil {
		t.Fatal(err)
	}
	hidden := false
	selection.Policies = map[string]toolregistry.Policy{
		definition.Name: {
			Visible: &hidden,
		},
	}
	installed.closeFailure.Store(true)
	// Restore cleanup admission even if an assertion aborts this test.
	t.Cleanup(func() { installed.closeFailure.Store(false) })
	if err := catalog.ConfigureSession(t.Context(), "agent", selection); err != nil {
		t.Fatal(err)
	}
	refresh := func() runtimeactor.Definitions {
		return ask[runtimeactor.Definitions](t, system, sender.Runtime(), runtimeactor.RefreshConfiguration{
			CreationID: "creation",
			ID:         "update",
		})
	}
	failed := awaitProviderRefresh(t, refresh, func(result runtimeactor.Definitions) bool {
		return result.Err != nil
	})
	if failed.Err == nil || installed.closes.Load() == 0 {
		t.Fatal("expected unfinished tool retirement")
	}
	// The next target introduces the first MCP connection. There is no old MCP
	// binding whose retirement could prove whether the new target was applied.
	if err := catalog.ConfigureSession(t.Context(), "agent", toolregistry.Catalog{
		MCP: []mcpclient.Config{configuration},
	}); err != nil {
		t.Fatal(err)
	}
	installed.closeFailure.Store(false)
	result := refresh()
	name := mcpclient.ToolName("fixture", "read")
	if result.Err != nil || !slices.Equal(result.Value.KnownNames, []string{name}) {
		t.Fatalf("old recovery acknowledged without applying the current target: %+v", result)
	}
	if len(result.Value.Schemas) != 1 || result.Value.Schemas[0].Name != name {
		t.Fatalf("new MCP definition was not visible to Prompt: %+v", result)
	}
	invokeMCP(t, system, sender.Runtime(), "agent", name, remote.result)
	removed := ask[catalogactor.ToolAddress](t, system, registry, catalogactor.FindTool{
		AgentID: "agent",
		Name:    definition.Name,
	})
	if removed.Found {
		t.Fatal("obsolete local tool remained executable")
	}
}

func TestRuntimeGlobalMCPDefinitionsReachPromptAndIndependentAgentStaysEmpty(t *testing.T) {
	remote, configuration := serveMCP(t, "global result")
	system, registry, _ := startCatalog(t, true, nil)
	configured := ask[catalogactor.Completion](t, system, registry, catalogactor.RegisterSource{
		Name: "mcp-config",
		Catalog: toolregistry.Catalog{
			MCP: []mcpclient.Config{configuration},
		},
	})
	if configured.Err != nil {
		t.Fatal(configured.Err)
	}
	ordinary, _ := newMCPRuntime(t, system, registry, "ordinary", nil, nil)
	independent, _ := newMCPRuntime(t, system, registry, "independent", &toolregistry.Catalog{}, nil)
	if err := startClient(t, system, ordinary, t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := startClient(t, system, independent, t.Context()); err != nil {
		t.Fatal(err)
	}
	if remote.listens.Load() != 1 {
		t.Fatal("independent empty Agent inherited the global MCP connection")
	}
	empty := ask[runtimeactor.Definitions](t, system, independent.Runtime(), runtimeactor.ReadDefinitions{
		CreationID: "creation",
	})
	if empty.Err != nil || len(empty.Value.KnownNames) != 0 {
		t.Fatalf("independent tools: %+v", empty)
	}
	address, err := goakt.GrainOf[*promptactor.RuntimeGrain](t.Context(), system, promptactor.RuntimeName("ordinary"), goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	initialized := requestPrompt[promptactor.RuntimeInitialized](t, system, address, promptactor.InitializeRuntime{
		AgentID:      "ordinary",
		CreationID:   "creation",
		Config:       &systemprompt.Config{},
		ToolsRuntime: ordinary.Runtime(),
	})
	if initialized.Err != nil {
		t.Fatal(initialized.Err)
	}
	t.Cleanup(func() {
		if result := requestPrompt[promptactor.Completion](t, system, address, promptactor.CloseRuntime{}); result.Err != nil {
			t.Error(result.Err)
		}
	})
	before := requestPrompt[promptactor.Assembled](t, system, address, promptactor.Assemble{
		ID: "before",
	})
	name := mcpclient.ToolName("fixture", "read")
	if before.Err != nil || len(before.Prompt.Tools) != 1 || before.Prompt.Tools[0].Name != name {
		t.Fatalf("MCP tools missing from initial Prompt: %+v", before)
	}
	invokeMCP(t, system, ordinary.Runtime(), "ordinary", name, remote.result)
	names := []string{"search"}
	remote.names.Store(&names)
	remote.notices <- struct{}{}
	added := mcpclient.ToolName("fixture", "search")
	updated := awaitProviderRefresh(t, func() runtimeactor.Definitions {
		return ask[runtimeactor.Definitions](t, system, ordinary.Runtime(), runtimeactor.RefreshConfiguration{
			CreationID: "creation",
			ID:         "next-turn",
		})
	}, func(result runtimeactor.Definitions) bool {
		return result.Err != nil || slices.Contains(result.Value.KnownNames, added)
	})
	if updated.Err != nil {
		t.Fatal(updated.Err)
	}
	// This is the same acknowledged Definitions -> ApplyConfiguration contract
	// used by AgentLoop at its Turn boundary, with real Runtime-produced values.
	applied := requestPrompt[promptactor.Completion](t, system, address, promptactor.ApplyConfiguration{
		AgentID: "ordinary",
		Definitions: systemprompt.ToolDefinitions{
			Schemas:    updated.Value.Schemas,
			KnownNames: updated.Value.KnownNames,
		},
	})
	if applied.Err != nil {
		t.Fatal(applied.Err)
	}
	after := requestPrompt[promptactor.Assembled](t, system, address, promptactor.Assemble{
		ID: "after",
	})
	if after.Err != nil || len(after.Prompt.Tools) != 1 || after.Prompt.Tools[0].Name != added {
		t.Fatalf("MCP refresh missing from Prompt: %+v", after)
	}
	if len(before.Prompt.Tools) != 1 || before.Prompt.Tools[0].Name != name {
		t.Fatal("refresh mutated a previous Prompt assembly")
	}
	invokeMCP(t, system, ordinary.Runtime(), "ordinary", added, remote.result)
}

// Prompt lifecycle uses asynchronous Grain requests in production.
type promptRequest struct {
	address *goakt.GrainIdentity
	command any
}

type promptReply struct {
	value any
	err   error
}

type promptCaller struct {
	replies chan promptReply
}

func (*promptCaller) PreStart(*goakt.Context) error { return nil }
func (*promptCaller) PostStop(*goakt.Context) error { return nil }
func (sender *promptCaller) Receive(rc *goakt.ReceiveContext) {
	if request, ok := rc.Message().(promptRequest); ok {
		replies := sender.replies
		rc.RequestGrain(request.address, request.command).Then(func(value any, err error) {
			replies <- promptReply{
				value: value,
				err:   err,
			}
		})
	}
}

func requestPrompt[T any](t *testing.T, system goakt.ActorSystem, address *goakt.GrainIdentity, command any) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	replies := make(chan promptReply, 1)
	sender, err := system.Spawn(ctx, "prompt-request-"+uuid.NewString(), &promptCaller{
		replies: replies,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Shutdown(context.Background())
	if err := goakt.Tell(ctx, sender, promptRequest{
		address: address,
		command: command,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case reply := <-replies:
		value, ok := reply.value.(T)
		if reply.err != nil || !ok {
			t.Fatalf("Prompt response %T: %v", reply.value, reply.err)
		}
		return value
	case <-ctx.Done():
		t.Fatal("Prompt request did not settle")
		var zero T
		return zero
	}
}
