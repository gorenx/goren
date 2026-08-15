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

	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentdefaultmodel"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/approval"
	protocol "github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sessionpersistence "github.com/gorenx/goren/session/persistence"
	sessionprojection "github.com/gorenx/goren/session/projection"
	sessiontitle "github.com/gorenx/goren/session/title"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/toolaskuser"
	toolscore "github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/userquestions"
	"github.com/gorenx/goren/workspace"
)

type probePlugin struct {
	body func(context.Context, *plugin.Scope) error
}

func (instance probePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "assembly-probe",
		Requires: []plugin.ServiceRef{
			agentcore.Service.Ref(), agentdefaultmodel.Service.Ref(), agentloop.Service.Ref(), approval.Service.Ref(), serverServiceKey.Ref(), llm.Service.Ref(), session.StoreService.Ref(), sessionpersistence.Service.Ref(), sessionprojection.Service.Ref(), sessiontitle.Service.Ref(), systemprompt.Service.Ref(), toolscore.Service.Ref(), userquestions.Service.Ref(), workspace.Service.Ref(),
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
	want := []string{
		AgentFactoryName, AgentDefaultModelFactoryName, AgentLoopFactoryName,
		ConnectionFactoryName, APIProxyFactoryName, LLMFactoryName, DeepSeekFactoryName,
		LLMRetryFactoryName,
		SessionFactoryName, SessionPersistenceFactoryName, SessionProjectionFactoryName, SessionTitleFactoryName,
		SystemPromptFactoryName, ToolAskUserFactoryName,
		ToolsFactoryName, ApprovalFactoryName, UserQuestionsFactoryName,
		WorkspaceFactoryName, WebFrontendFactoryName,
	}
	if got := registry.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("factory names = %#v, want %#v", got, want)
	}
	for _, excludedFactory := range []string{
		"@deepseek-ai/dsh-client", "@deepseek-ai/dsh-sdk",
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
		{label: "agent unknown", factoryName: AgentFactoryName, input: `{"unknown":true}`, wantMessage: "unknown field"},
		{label: "default model unknown", factoryName: AgentDefaultModelFactoryName, input: `{"provider":"p","model":"m","unknown":true}`, wantMessage: "unknown field"},
		{label: "default model empty", factoryName: AgentDefaultModelFactoryName, input: `{"provider":"p","model":""}`, wantMessage: "must be non-empty"},
		{label: "agent loop unknown", factoryName: AgentLoopFactoryName, input: `{"unknown":true}`, wantMessage: "unknown field"},
		{label: "agent loop nested unknown", factoryName: AgentLoopFactoryName, input: `{"agents":[{"id":"a","unknown":true}]}`, wantMessage: "unknown field"},
		{label: "agent loop parallel limit", factoryName: AgentLoopFactoryName, input: `{"maxParallelToolCalls":0}`, wantMessage: "positive integer"},
		{label: "agent loop identities", factoryName: AgentLoopFactoryName, input: `{"agents":[{"id":"a","sessionId":"s","resumeSessionId":"r"}]}`, wantMessage: "mutually exclusive"},
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
		{label: "approval unknown", factoryName: ApprovalFactoryName, input: `{"unknown":true}`, wantMessage: "unknown field"},
		{label: "approval policy", factoryName: ApprovalFactoryName, input: `{"policy":"sometimes"}`, wantMessage: "must be ask or never"},
		{label: "questions unknown", factoryName: UserQuestionsFactoryName, input: `{"unknown":true}`, wantMessage: "unknown field"},
		{label: "ask tool unknown", factoryName: ToolAskUserFactoryName, input: `{"unknown":true}`, wantMessage: "unknown field"},
		{label: "llm unknown", factoryName: LLMFactoryName, input: `{"unknown":true}`, wantMessage: "unknown field"},
		{label: "llm retry unknown", factoryName: LLMRetryFactoryName, input: `{"unknown":true}`, wantMessage: "unknown key"},
		{label: "llm retry misplaced policy", factoryName: LLMRetryFactoryName, input: `{"retryPolicy":{"mode":"always"}}`, wantMessage: "belongs under each provider"},
		{label: "llm retry null", factoryName: LLMRetryFactoryName, input: `null`, wantMessage: "must be an object"},
		{label: "projection unknown", factoryName: SessionProjectionFactoryName, input: `{"unknown":true}`, wantMessage: "unknown field"},
		{label: "title unknown", factoryName: SessionTitleFactoryName, input: `{"fallbackMaxWords":5,"fallbackMaxBytes":40,"maxTitleBytes":80,"unknown":true}`, wantMessage: "unknown field"},
		{label: "title invalid cap", factoryName: SessionTitleFactoryName, input: `{"fallbackMaxWords":5,"fallbackMaxBytes":81,"maxTitleBytes":80}`, wantMessage: "must not exceed"},
		{label: "persistence unknown", factoryName: SessionPersistenceFactoryName, input: `{"path":"/tmp/sessions.sqlite","unknown":true}`, wantMessage: "unknown field"},
		{label: "persistence empty path", factoryName: SessionPersistenceFactoryName, input: `{"path":""}`, wantMessage: "path must be non-empty"},
		{label: "persistence journal", factoryName: SessionPersistenceFactoryName, input: `{"path":"/tmp/sessions.sqlite","journalMode":"memory"}`, wantMessage: "journalMode must be"},
		{label: "workspace unknown", factoryName: WorkspaceFactoryName, input: `{"path":"/tmp/workspaces.sqlite","unknown":true}`, wantMessage: "unknown field"},
		{label: "workspace empty path", factoryName: WorkspaceFactoryName, input: `{"path":""}`, wantMessage: "path must be non-empty"},
		{label: "workspace journal", factoryName: WorkspaceFactoryName, input: `{"path":"/tmp/workspaces.sqlite","journalMode":"memory"}`, wantMessage: "journalMode must be"},
		{label: "web unknown", factoryName: WebFrontendFactoryName, input: `{"unknown":true}`, wantMessage: "unknown field"},
		{label: "deepseek unknown", factoryName: DeepSeekFactoryName, input: `{"unknown":true}`, wantMessage: "unknown field"},
		{label: "deepseek nested unknown", factoryName: DeepSeekFactoryName, input: `{"models":[{"id":"m","unknown":true}]}`, wantMessage: "unknown field"},
		{label: "deepseek disabled high", factoryName: DeepSeekFactoryName, input: `{"thinking":"disabled","reasoningEffort":"high"}`, wantMessage: "only reasoningEffort off"},
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
	dataDirectory := t.TempDir()
	declarations, err := DefaultSpecs(
		"127.0.0.1:0", "0.1.0-rc.5",
		dataDirectory+"/sessions.sqlite", dataDirectory+"/workspaces.sqlite",
	)
	if err != nil {
		t.Fatal(err)
	}
	engine := plugin.NewRuntime()
	if _, err := Load(requestContext, engine, registry, declarations); err != nil {
		t.Fatal(err)
	}
	serverAddress := ""
	probe := probePlugin{body: func(_ context.Context, pluginScope *plugin.Scope) error {
		agentService, found := plugin.Require(pluginScope, agentcore.Service)
		if !found {
			t.Fatal("agents service is unavailable")
		}
		if liveAgents := agentService.List(); len(liveAgents) != 0 {
			t.Fatalf("default live Agents = %#v", liveAgents)
		}
		loopRuntime, found := plugin.Require(pluginScope, agentloop.Service)
		if !found || loopRuntime.MaxParallelToolCalls() != agentloop.DefaultMaxParallelToolCalls {
			t.Fatalf("agentLoop service = %#v, found = %t", loopRuntime, found)
		}
		handle, createErr := agentService.Create(requestContext, pluginScope, agentcore.CreateOptions{
			SessionID: "assembly-agent", AgentOptions: agentcore.Options{Provider: "deepseek-official", Model: "deepseek-chat"},
		})
		if createErr != nil {
			return createErr
		}
		if _, found := agentService.Get("assembly-agent"); !found {
			t.Fatal("Agent Loop factory did not publish the requested Agent")
		}
		if disposeErr := handle.Dispose(requestContext); disposeErr != nil {
			return disposeErr
		}
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
		if durability, found := plugin.Require(pluginScope, sessionpersistence.Service); !found || durability == nil {
			t.Fatal("sessionPersistence service is unavailable")
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
		if projections := toolService.Schemas(plugin.ScopeKey{}); len(projections) != 1 || projections[0].Name != toolaskuser.Name {
			t.Fatalf("default tool schemas = %#v", projections)
		}
		if approvalService, found := plugin.Require(pluginScope, approval.Service); !found || approvalService == nil {
			t.Fatal("approval service is unavailable")
		}
		if questionService, found := plugin.Require(pluginScope, userquestions.Service); !found || questionService == nil {
			t.Fatal("userQuestions service is unavailable")
		}
		llmService, found := plugin.Require(pluginScope, llm.Service)
		if !found {
			t.Fatal("llm service is unavailable")
		}
		if providers := llmService.ListProviders(); !reflect.DeepEqual(providers, []llm.ProviderInfo{{ID: "deepseek-official", Name: "DeepSeek"}}) {
			t.Fatalf("default llm providers = %#v", providers)
		}
		if configurable := llmService.ListConfigurableProviders(); !reflect.DeepEqual(configurable, []llm.ConfigurableProvider{{
			Provider: "deepseek-official", DisplayName: "DeepSeek", SettingsNS: "llm-deepseek", SettingsPath: []string{},
		}}) {
			t.Fatalf("default configurable providers = %#v", configurable)
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
		description.CWD != "/contract-workspace" || description.Provider != "deepseek-official" ||
		description.Model != "deepseek-v4-flash" || description.AttachedSessions != 0 {
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
	dataDirectory := t.TempDir()
	declarations, err := DefaultSpecs(
		reservedListener.Addr().String(), "test",
		dataDirectory+"/sessions.sqlite", dataDirectory+"/workspaces.sqlite",
	)
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
