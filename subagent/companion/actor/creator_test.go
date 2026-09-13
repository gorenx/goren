package companionactor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/gorenx/goren/agent"
	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/llm"
	llmactor "github.com/gorenx/goren/llm/actor"
	"github.com/gorenx/goren/llm/modelconfig"
	"github.com/gorenx/goren/mcpclient"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/settings"
	companions "github.com/gorenx/goren/subagent/companion"
	"github.com/gorenx/goren/subagent/tools/control"
	"github.com/gorenx/goren/systemprompt"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	"github.com/gorenx/goren/tools/tool"
	"github.com/gorenx/goren/userquestions"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// creatorMCP replaces only the remote wire peer; each child opens its real
// Connection and invokes through its ordinary Tool Runtime and ToolActor.
type creatorMCP struct {
	calls atomic.Int32
	opens atomic.Int32
}

type creatorQuestions struct{}

func (*creatorQuestions) Ask(context.Context, userquestions.Request) (userquestions.Answer, error) {
	return userquestions.Answer{}, errors.New("test: unexpected MCP question")
}

func (remote *creatorMCP) ServeHTTP(writer http.ResponseWriter, incoming *http.Request) {
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.NewDecoder(incoming.Body).Decode(&envelope); err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	var result string
	switch envelope.Method {
	case "server/discover":
		remote.opens.Add(1)
		result = `{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{}}}`
	case "tools/list":
		result = `{"resultType":"complete","tools":[{"name":"read","inputSchema":{"type":"object"}}]}`
	case "tools/call":
		remote.calls.Add(1)
		result = `{"resultType":"complete","content":[{"type":"text","text":"independent MCP result"}]}`
	case "subscriptions/listen":
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/subscriptions/acknowledged\",\"params\":{\"notifications\":{\"toolsListChanged\":true},\"_meta\":{\"io.modelcontextprotocol/subscriptionId\":\"sub\"}}}\n\n")
		writer.(http.Flusher).Flush()
		<-incoming.Context().Done()
		return
	default:
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":%s}`, envelope.ID, result)
}

func prepareCreatorModels(t *testing.T, current *stewardship) {
	if err := runtimeactor.Install(current.system, &creatorQuestions{}); err != nil {
		t.Fatal(err)
	}
	models, err := goakt.GrainOf[*llmactor.Grain](t.Context(), current.system, "creator-models",
		goakt.WithLongLivedGrain(),
		goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))),
	)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := modelconfig.NewCatalog(modelconfig.Definition{
		Default: llm.CallConfig{
			Provider: "shared",
			Model:    "parent-default",
		},
		Providers: []modelconfig.Provider{{
			ID: "shared",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := request[llmactor.Completion](t, current.system, models, llmactor.InitializeModels{
		Catalog: catalog,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	current.input.ModelConfig = models
}

func TestCreatorRestoresNamedConfigurationThroughOrdinaryOwners(t *testing.T) {
	remote := &creatorMCP{}
	server := httptest.NewServer(remote)
	t.Cleanup(server.Close)
	fixture, _, _ := messagingFixture(t, func(current *stewardship) tool.Providers {
		prepareCreatorModels(t, current)
		return nil
	})
	starter := &Creator{
		System:   fixture.system,
		Registry: fixture.authority,
		Tools:    fixture.input.ToolsRegistry,
		Settings: fixture.creator.settings,
		Storage:  fixture.creator.storage,
	}
	if configured := request[catalogactor.Completion](t, fixture.system, fixture.input.ToolsRegistry, catalogactor.RegisterSource{
		Name: "creator-defaults",
		Catalog: toolregistry.Catalog{
			Schemas: []llm.ToolSchema{control.MessageSchema()},
			MCP: []mcpclient.Config{{
				ServerName: "global",
				Transport:  "streamable-http",
				URL:        server.URL,
			}},
		},
	}); configured.Err != nil {
		t.Fatal(configured.Err)
	}
	stewardProvider, sender, parent := installParent(t, fixture, fixture.authority, starter)
	awaitInstallation(t, fixture, stewardProvider, parent.Creation, Ready)
	initial := settings.Values{
		Model: agent.Options{
			Model:     "review-v1",
			MaxTokens: new(512),
		},
		Prompts: map[string]settings.PromptConfig{
			"default": {
				Persona: new("independent prompt v1"),
			},
		},
		MCP: map[string]settings.MCPConfig{
			"docs": {
				Enabled: true,
				Connection: mcpclient.Config{
					ServerName: "docs",
					Transport:  "streamable-http",
					URL:        server.URL,
				},
			},
		},
		Tools: map[string]tools.Config{
			"send_message": {
				Enabled: new(false),
			},
			"mcp_docs_read": {
				Visible: new(false),
			},
		},
	}
	if _, err := sender.Create(t.Context(), Create{
		Name:    "reviewer",
		Enabled: true,
		Initial: initial,
	}); err != nil {
		t.Fatal(err)
	}
	first := awaitPhase(t, sender, "reviewer", Ready)
	conversation, err := goakt.GrainOf[*sessionactor.Grain](t.Context(), fixture.system,
		sessionactor.RuntimeName(string(first.Creation.AgentID)), goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	metadata := request[sessionactor.View](t, fixture.system, conversation, sessionactor.Read{
		CreationID: first.Creation.CreationID,
	})
	if metadata.Err != nil || metadata.Header.CWD != nil {
		t.Fatalf("child of parent without CWD acquired a directory: %+v", metadata)
	}
	assertConfiguration := func(current View, expected string) {
		t.Helper()
		selected := request[llmactor.ConfigurationResult](t, fixture.system, fixture.input.ModelConfig, llmactor.ReadConfiguration{
			AgentID: string(current.Creation.AgentID),
		})
		if selected.Err != nil || selected.Config.Provider != "shared" || selected.Config.Model != "review-"+expected || selected.Config.MaxTokens == nil || *selected.Config.MaxTokens != 512 {
			t.Fatalf("child model configuration: %+v", selected)
		}
		prompt, err := goakt.GrainOf[*promptactor.RuntimeGrain](t.Context(), fixture.system, promptactor.RuntimeName(string(current.Creation.AgentID)), goakt.WithLongLivedGrain())
		if err != nil {
			t.Fatal(err)
		}
		observed := request[promptactor.RuntimeView](t, fixture.system, prompt, promptactor.ReadRuntime{})
		if observed.Err != nil || observed.Config.Persona != "independent prompt "+expected {
			t.Fatalf("child prompt configuration: %+v", observed)
		}
	}
	assertConfiguration(first, "v1")
	if remote.opens.Load() != 1 {
		t.Fatal("configuration opened connections outside the child Session")
	}
	target, err := goakt.GrainOf[*runtimeactor.Runtime](t.Context(), fixture.system, runtimeactor.RuntimeName(string(first.Creation.AgentID)), goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	exposed := request[runtimeactor.Definitions](t, fixture.system, target, runtimeactor.ReadDefinitions{
		CreationID: first.Creation.CreationID,
	})
	if exposed.Err != nil || len(exposed.Value.Schemas) != 1 || exposed.Value.Schemas[0].Name != "report" || !slices.Contains(exposed.Value.KnownNames, "mcp_docs_read") {
		t.Fatalf("independent tool configuration: %+v", exposed)
	}
	called := fixture.invoke(t, t.Context(), first.Creation, "mcp_docs_read", json.RawMessage(`{}`))
	if called.Err != nil || remote.calls.Load() != 1 {
		t.Fatalf("child MCP tool did not execute: %+v", called)
	}
	if _, ok := called.Result.SuccessValue(); !ok {
		t.Fatal("child MCP returned a failed tool result")
	}
	initial.Model.Model = "review-v2"
	initial.Prompts["default"] = settings.PromptConfig{
		Persona: new("independent prompt v2"),
	}
	saved, err := starter.Settings.Describe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	version := saved.State.Version(settings.Group{
		Section: settings.AgentSection,
		Name:    first.Companion.Configuration,
	})
	if _, err := starter.Settings.UpdateAgent(t.Context(), first.Companion.Configuration, version, initial); err != nil {
		t.Fatal(err)
	}
	assertConfiguration(first, "v1")
	if _, err := sender.Restart(t.Context(), Restart{
		Name:               "reviewer",
		ExpectedRevision:   first.Companion.Revision,
		ExpectedCreationID: first.Creation.CreationID,
	}); err != nil {
		t.Fatal(err)
	}
	restored := awaitPhase(t, sender, "reviewer", Ready)
	if restored.Creation.AgentID != first.Creation.AgentID || restored.Creation.CreationID == first.Creation.CreationID || remote.opens.Load() != 2 {
		t.Fatal("restart did not restore the same Session with new resources")
	}
	assertConfiguration(restored, "v2")
	selected := request[llmactor.ConfigurationResult](t, fixture.system, fixture.input.ModelConfig, llmactor.ReadConfiguration{
		AgentID: string(parent.Creation.AgentID),
	})
	if selected.Err != nil || selected.Config.Model != "parent-default" {
		t.Fatal("child configuration changed the parent model")
	}
	for _, name := range []string{"report", "not_installed"} {
		if _, err := sender.Create(t.Context(), Create{
			Name:    name,
			Enabled: true,
			Initial: settings.Values{
				Tools: map[string]tools.Config{
					name: {
						Enabled: new(false),
					},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
		failed := awaitPhase(t, sender, name, StartFailed)
		missing := request[agentregistry.Ready](t, fixture.system, fixture.authority, agentregistry.FindAgent{
			AgentID: failed.Companion.ChildSessionID,
		})
		if failed.Err == nil || !errors.Is(missing.Err, agentregistry.ErrCreation) {
			t.Fatalf("invalid tool selection created a child: %+v", failed)
		}
	}
	for _, failureAt := range []string{"stale-parent", "missing-config", "cancelled"} {
		t.Run(failureAt, func(t *testing.T) {
			expected := parent.Creation
			name := first.Companion.Configuration
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch failureAt {
			case "stale-parent":
				expected.CreationID = "obsolete"
			case "missing-config":
				name = "missing"
			case "cancelled":
				cancel()
			}
			child := agentregistry.Creation{
				AgentID:    "unadmitted",
				CreationID: failureAt,
			}
			if _, err := starter.Start(ctx, expected, child, name, nil); err == nil {
				t.Fatal("invalid creation succeeded")
			}
			missing := request[agentregistry.Ready](t, fixture.system, fixture.authority, agentregistry.FindAgent{
				AgentID: child.AgentID,
			})
			if !errors.Is(missing.Err, agentregistry.ErrCreation) {
				t.Fatalf("preparation failure reserved a child: %+v", missing)
			}
		})
	}
}

func TestCreatorInheritsCurrentParentConfigurationForDefaultCompanion(t *testing.T) {
	remote := &creatorMCP{}
	server := httptest.NewServer(remote)
	t.Cleanup(server.Close)
	fixture, _, _ := messagingFixture(t, func(current *stewardship) tool.Providers {
		prepareCreatorModels(t, current)
		return nil
	})
	connection := mcpclient.Config{
		ServerName: "docs",
		Transport:  "streamable-http",
		URL:        server.URL,
	}
	fixture.input.Tools = &toolregistry.Catalog{
		Schemas: []llm.ToolSchema{
			control.MessageSchema(),
			control.ListSchema(),
			{
				Name:       "disabled",
				Parameters: json.RawMessage(`{"type":"object"}`),
			},
		},
		MCP: []mcpclient.Config{connection},
		Policies: map[string]toolregistry.Policy{
			"disabled": {
				Enabled: new(false),
			},
		},
	}
	fixture.input.Prompt = &systemprompt.Config{
		Persona: "parent persona",
	}
	starter := &Creator{
		System:   fixture.system,
		Registry: fixture.authority,
		Tools:    fixture.input.ToolsRegistry,
		Model:    fixture.input.ModelConfig,
		Settings: fixture.creator.settings,
		Storage:  fixture.creator.storage,
	}
	if _, err := starter.Settings.UpdateAgent(t.Context(), "review-template", 0, settings.Values{}); err != nil {
		t.Fatal(err)
	}
	policy := companions.Config{
		Enabled:       true,
		Configuration: "review-template",
		Inherit: companions.Inheritance{
			Model:  true,
			Prompt: true,
			Tools:  companions.InheritAllTools,
			MCP:    true,
		},
	}
	if _, err := starter.Settings.UpdateCompanion(t.Context(), "reviewer", 0, policy); err != nil {
		t.Fatal(err)
	}
	stewardProvider, sender, parentInput := installParent(t, fixture, fixture.authority, starter)
	awaitInstallation(t, fixture, stewardProvider, parentInput.Creation, Ready)
	child := awaitPhase(t, sender, "reviewer", Ready)
	view, err := starter.Settings.Describe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	saved := view.Target.Agents[child.Companion.Configuration]
	if saved.Model.Provider != "shared" || saved.Model.Model != "parent-default" ||
		saved.Prompts["default"].Persona == nil || *saved.Prompts["default"].Persona != "parent persona" ||
		len(saved.MCP) != 1 || saved.MCP["docs"].Connection.URL != server.URL || len(saved.Tools) != 4 ||
		saved.Tools["send_message"].Enabled == nil || !*saved.Tools["send_message"].Enabled ||
		saved.Tools["disabled"].Enabled == nil || *saved.Tools["disabled"].Enabled ||
		saved.Tools["mcp_docs_read"].Visible == nil || !*saved.Tools["mcp_docs_read"].Visible {
		t.Fatalf("explicit inheritance did not freeze adopted values: %+v", saved)
	}
	if remote.opens.Load() != 2 {
		t.Fatal("parent and child did not acquire independent Session connections")
	}
	for _, name := range []string{"send_message", "disabled"} {
		if _, err := starter.Resolve(t.Context(), parentInput.Creation, policy, settings.Values{
			Tools: map[string]tools.Config{
				name: {},
			},
		}); err == nil {
			t.Fatalf("dedicated tool duplicated inherited name %q", name)
		}
	}
	dedicated := policy.Clone()
	dedicated.Inherit.MCP = false
	resolved, err := starter.Resolve(t.Context(), parentInput.Creation, dedicated, settings.Values{
		MCP: map[string]settings.MCPConfig{
			"docs": {
				Enabled:    true,
				Connection: connection,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sender.Create(t.Context(), Create{
		Name:    "dedicated-mcp",
		Enabled: true,
		Initial: resolved,
	}); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, sender, "dedicated-mcp", Ready)
	pending := fixture.input.Tools.Clone()
	pending.Schemas = append(pending.Schemas, llm.ToolSchema{
		Name:       "pending",
		Parameters: json.RawMessage(`{"type":"object"}`),
	})
	pending.Policies = map[string]toolregistry.Policy{
		"send_message": {
			Visible: new(false),
		},
	}
	configured := request[catalogactor.Completion](t, fixture.system, fixture.input.ToolsRegistry, catalogactor.ConfigureSession{
		SessionID: parentInput.Creation.AgentID,
		Catalog:   pending,
	})
	if configured.Err != nil {
		t.Fatal(configured.Err)
	}
	adopted, err := starter.Resolve(t.Context(), parentInput.Creation, policy, settings.Values{})
	if err != nil {
		t.Fatal(err)
	}
	if _, found := adopted.Tools["pending"]; found || adopted.Tools["send_message"].Visible == nil || !*adopted.Tools["send_message"].Visible {
		t.Fatalf("inheritance copied unapplied Registry input instead of the parent Runtime: %+v", adopted.Tools)
	}
}

func TestCreatorIncludesParentSessionSchemaAdditions(t *testing.T) {
	fixture, _, _ := messagingFixture(t, func(current *stewardship) tool.Providers {
		prepareCreatorModels(t, current)
		return nil
	})
	fixture.input.Tools = &toolregistry.Catalog{
		InheritGlobal: true,
		Schemas: []llm.ToolSchema{
			{
				Name:        "session_tool",
				Description: "parent Session addition",
				Parameters:  json.RawMessage(`{"type":"object"}`),
			},
		},
		Policies: map[string]toolregistry.Policy{
			"session_tool": {
				Enabled: new(false),
			},
		},
	}
	starter := &Creator{
		System:   fixture.system,
		Registry: fixture.authority,
		Tools:    fixture.input.ToolsRegistry,
		Model:    fixture.input.ModelConfig,
		Settings: fixture.creator.settings,
		Storage:  fixture.creator.storage,
	}
	stewardProvider, sender, parent := installParent(t, fixture, fixture.authority, starter)
	awaitInstallation(t, fixture, stewardProvider, parent.Creation, Ready)
	resolved, err := starter.Resolve(t.Context(), parent.Creation, companions.Config{
		Inherit: companions.Inheritance{
			Tools: companions.InheritAllTools,
		},
	}, settings.Values{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sender.Create(t.Context(), Create{
		Name:    "reviewer",
		Enabled: true,
		Initial: resolved,
	}); err != nil {
		t.Fatal(err)
	}
	child := awaitPhase(t, sender, "reviewer", Ready)
	selected := request[catalogactor.Configuration](t, fixture.system, fixture.input.ToolsRegistry, catalogactor.ReadConfiguration{
		SessionID: child.Companion.ChildSessionID,
	})
	index := slices.IndexFunc(selected.Value.Global.Schemas, func(entry llm.ToolSchema) bool {
		return entry.Name == "session_tool"
	})
	if index < 0 || selected.Value.Global.Schemas[index].Description != "parent Session addition" {
		t.Fatal("child lost the inherited Session schema")
	}
	if enabled := selected.Value.Global.Policies["session_tool"].Enabled; enabled == nil || *enabled {
		t.Fatal("child lost the inherited disabled policy")
	}
}
