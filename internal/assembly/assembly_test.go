package assembly

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentdefaultmodel"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/commands"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/compaction/basic"
	compactioncommand "github.com/gorenx/goren/compaction/command"
	"github.com/gorenx/goren/compaction/toolresultpruner"
	protocol "github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/credentials"
	connectionhost "github.com/gorenx/goren/internal/connection"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/deepseek"
	"github.com/gorenx/goren/llm/retry"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/session/query"
	"github.com/gorenx/goren/session/title"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/fork"
	"github.com/gorenx/goren/subagent/spawn"
	"github.com/gorenx/goren/subagent/tools/control"
	subagentdelegation "github.com/gorenx/goren/subagent/tools/delegation"
	"github.com/gorenx/goren/subagent/tools/report"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/toolaskuser"
	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/userquestions"
	"github.com/gorenx/goren/web"
	"github.com/gorenx/goren/workspace"
)

const probeFactoryName = "test/assembly-probe"
const invalidFactoryName = "test/invalid-factory"

type serviceProbe struct {
	plugin.Base
	agents       agent.Registry
	constructor  agent.Constructor
	defaultModel agentdefaultmodel.DefaultModel
	approvals    approval.Approval
	apiProxy     apiproxy.Service
	commandPlane commands.Registry
	credentials  credentials.Provider
	models       llm.LlmRuntime
	compactor    compaction.Engine
	meter        tokenmeter.Meter
	pruner       toolresultpruner.Pruner
	sessions     session.LiveStore
	durability   persistence.Persistence
	projections  projection.Registry
	queries      query.QueryService
	titles       title.TitleService
	prompts      systemprompt.Assembler
	subagents    subagent.ChildDirectory
	toolRuntime  tools.ToolRuntime
	questions    userquestions.UserQuestions
	workspaces   workspace.Registry
}

func (*serviceProbe) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: probeFactoryName,
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[agent.Registry](),
			plugin.ServiceOf[agent.Constructor](),
			plugin.ServiceOf[agentdefaultmodel.DefaultModel](),
			plugin.ServiceOf[approval.Approval](),
			plugin.ServiceOf[apiproxy.Service](),
			plugin.ServiceOf[commands.Registry](),
			plugin.ServiceOf[credentials.Provider](),
			plugin.ServiceOf[llm.LlmRuntime](),
			plugin.ServiceOf[compaction.Engine](),
			plugin.ServiceOf[tokenmeter.Meter](),
			plugin.ServiceOf[toolresultpruner.Pruner](),
			plugin.ServiceOf[session.LiveStore](),
			plugin.ServiceOf[persistence.Persistence](),
			plugin.ServiceOf[projection.Registry](),
			plugin.ServiceOf[query.QueryService](),
			plugin.ServiceOf[title.TitleService](),
			plugin.ServiceOf[systemprompt.Assembler](),
			plugin.ServiceOf[subagent.ChildDirectory](),
			plugin.ServiceOf[tools.ToolRuntime](),
			plugin.ServiceOf[userquestions.UserQuestions](),
			plugin.ServiceOf[workspace.Registry](),
		},
	}
}

func (probe *serviceProbe) Apply(requestContext context.Context) error {
	var err error
	if probe.agents, err = plugin.Require[agent.Registry](probe); err != nil {
		return err
	}
	if probe.constructor, err = plugin.Require[agent.Constructor](probe); err != nil {
		return err
	}
	if probe.defaultModel, err = plugin.Require[agentdefaultmodel.DefaultModel](probe); err != nil {
		return err
	}
	if probe.approvals, err = plugin.Require[approval.Approval](probe); err != nil {
		return err
	}
	if probe.apiProxy, err = plugin.Require[apiproxy.Service](probe); err != nil {
		return err
	}
	if probe.commandPlane, err = plugin.Require[commands.Registry](probe); err != nil {
		return err
	}
	if probe.credentials, err = plugin.Require[credentials.Provider](probe); err != nil {
		return err
	}
	if probe.models, err = plugin.Require[llm.LlmRuntime](probe); err != nil {
		return err
	}
	if probe.compactor, err = plugin.Require[compaction.Engine](probe); err != nil {
		return err
	}
	if probe.meter, err = plugin.Require[tokenmeter.Meter](probe); err != nil {
		return err
	}
	if probe.pruner, err = plugin.Require[toolresultpruner.Pruner](probe); err != nil {
		return err
	}
	if probe.sessions, err = plugin.Require[session.LiveStore](probe); err != nil {
		return err
	}
	if probe.durability, err = plugin.Require[persistence.Persistence](probe); err != nil {
		return err
	}
	if probe.projections, err = plugin.Require[projection.Registry](probe); err != nil {
		return err
	}
	if probe.queries, err = plugin.Require[query.QueryService](probe); err != nil {
		return err
	}
	if probe.titles, err = plugin.Require[title.TitleService](probe); err != nil {
		return err
	}
	if probe.prompts, err = plugin.Require[systemprompt.Assembler](probe); err != nil {
		return err
	}
	if probe.subagents, err = plugin.Require[subagent.ChildDirectory](probe); err != nil {
		return err
	}
	if probe.toolRuntime, err = plugin.Require[tools.ToolRuntime](probe); err != nil {
		return err
	}
	if probe.questions, err = plugin.Require[userquestions.UserQuestions](probe); err != nil {
		return err
	}
	probe.workspaces, err = plugin.Require[workspace.Registry](probe)
	return err
}

func (*serviceProbe) Dispose(context.Context) error {
	return nil
}

type probeFactory struct {
	instance *serviceProbe
}

func (*probeFactory) Name() string {
	return probeFactoryName
}

func (builder *probeFactory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := createContext.Err(); err != nil {
		return nil, err
	}
	if err := pluginfactory.ValidateEmptyConfig(
		rawConfig,
		probeFactoryName,
	); err != nil {
		return nil, err
	}
	return builder.instance, nil
}

type invalidFactory struct {
	instance plugin.Plugin
}

func (*invalidFactory) Name() string {
	return invalidFactoryName
}

func (builder *invalidFactory) Create(
	context.Context,
	json.RawMessage,
) (plugin.Plugin, error) {
	return builder.instance, nil
}

type wrongPlugin struct {
	plugin.Base
}

func (*wrongPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "test/wrong-plugin",
	}
}

func (*wrongPlugin) Apply(context.Context) error {
	return nil
}

func (*wrongPlugin) Dispose(context.Context) error {
	return nil
}

func testDiagnostics(t *testing.T) *Diagnostics {
	t.Helper()
	reporter, err := NewDiagnostics(func(problem error) {
		t.Errorf("contained runtime failure: %v", problem)
	})
	if err != nil {
		t.Fatal(err)
	}
	return reporter
}

func newTestCatalog(t *testing.T, workingDirectory string) *pluginfactory.Catalog {
	t.Helper()
	directory, err := NewCatalog(Environment{
		WorkingDirectory: workingDirectory,
		Diagnostics:      testDiagnostics(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

func addProbe(
	t *testing.T,
	directory *pluginfactory.Catalog,
	specs []PluginSpec,
) (*serviceProbe, []PluginSpec) {
	t.Helper()
	probe := &serviceProbe{}
	if err := directory.Register(&probeFactory{
		instance: probe,
	}); err != nil {
		t.Fatal(err)
	}
	return probe, append(specs, PluginSpec{
		FactoryName: probeFactoryName,
		Config:      json.RawMessage(`{}`),
	})
}

func TestCatalogContainsOnlyCurrentServerSlice(t *testing.T) {
	t.Parallel()
	directory := newTestCatalog(t, t.TempDir())
	want := []string{
		agent.PluginName,
		agentdefaultmodel.PluginName,
		agentloop.PluginName,
		apiproxy.PluginName,
		approval.PluginName,
		commands.PluginName,
		basic.PluginName,
		compactioncommand.PluginName,
		toolresultpruner.PluginName,
		connectionhost.PluginName,
		credentials.PluginName,
		deepseek.PluginName,
		llm.PluginName,
		llmretry.PluginName,
		tokenmeter.PluginName,
		session.PluginName,
		persistence.PluginName,
		projection.PluginName,
		query.PluginName,
		title.PluginName,
		systemprompt.PluginName,
		subagent.PluginName,
		spawn.PluginName,
		fork.PluginName,
		subagentdelegation.PluginName,
		control.PluginName,
		report.PluginName,
		toolaskuser.PluginName,
		tools.PluginName,
		userquestions.PluginName,
		web.PluginName,
		workspace.PluginName,
	}
	sort.Strings(want)
	if got := directory.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Factory names = %#v, want %#v", got, want)
	}
	for _, excludedFactory := range []string{
		"@deepseek-ai/dsh-client",
		"@deepseek-ai/dsh-sdk",
		"@deepseek-ai/dsh-acp",
		"@deepseek-ai/dsh-mcp-client",
	} {
		if _, err := directory.Lookup(excludedFactory); err == nil {
			t.Fatalf("excluded Factory %q is registered", excludedFactory)
		}
	}
}

func TestBuildServerRejectsInvalidFactoryResults(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		instance    plugin.Plugin
		wantMessage string
	}{
		{
			name:        "nil Plugin",
			wantMessage: "returned a nil Plugin",
		},
		{
			name:        "mismatched Plugin identity",
			instance:    &wrongPlugin{},
			wantMessage: "returned Plugin \"test/wrong-plugin\"",
		},
	} {
		selectedCase := testCase
		t.Run(selectedCase.name, func(t *testing.T) {
			t.Parallel()
			directory := pluginfactory.NewCatalog()
			if err := directory.Register(&invalidFactory{
				instance: selectedCase.instance,
			}); err != nil {
				t.Fatal(err)
			}
			_, err := BuildServer(
				context.Background(),
				directory,
				[]PluginSpec{
					{
						FactoryName: invalidFactoryName,
						Config:      json.RawMessage(`{}`),
					},
				},
			)
			if err == nil || !strings.Contains(err.Error(), selectedCase.wantMessage) {
				t.Fatalf(
					"BuildServer error = %v, want containing %q",
					err,
					selectedCase.wantMessage,
				)
			}
		})
	}
}

func TestFactoriesOwnStrictTypedConfiguration(t *testing.T) {
	t.Parallel()
	directory := newTestCatalog(t, t.TempDir())
	testCases := []struct {
		label       string
		factoryName string
		input       string
		wantMessage string
	}{
		{
			label:       "connection unknown",
			factoryName: connectionhost.PluginName,
			input:       `{"listenAddress":"127.0.0.1:0","extra":true}`,
			wantMessage: "unknown field",
		},
		{
			label:       "connection wrong type",
			factoryName: connectionhost.PluginName,
			input:       `{"listenAddress":7}`,
			wantMessage: "cannot unmarshal",
		},
		{
			label:       "connection negative limit",
			factoryName: connectionhost.PluginName,
			input:       `{"listenAddress":"127.0.0.1:0","maxBodyBytes":-1}`,
			wantMessage: "must not be negative",
		},
		{
			label:       "connection timeout overflow",
			factoryName: connectionhost.PluginName,
			input:       `{"listenAddress":"127.0.0.1:0","gracefulTimeoutMillis":9223372036854775807}`,
			wantMessage: "must not exceed",
		},
		{
			label:       "agent unknown",
			factoryName: agent.PluginName,
			input:       `{"unknown":true}`,
			wantMessage: "unknown field",
		},
		{
			label:       "default model unknown",
			factoryName: agentdefaultmodel.PluginName,
			input:       `{"provider":"p","model":"m","unknown":true}`,
			wantMessage: "unknown field",
		},
		{
			label:       "default model empty",
			factoryName: agentdefaultmodel.PluginName,
			input:       `{"provider":"p","model":""}`,
			wantMessage: "must be non-empty",
		},
		{
			label:       "agent loop nested unknown",
			factoryName: agentloop.PluginName,
			input:       `{"agents":[{"id":"a","unknown":true}]}`,
			wantMessage: "unknown field",
		},
		{
			label:       "agent loop parallel limit",
			factoryName: agentloop.PluginName,
			input:       `{"maxParallelToolCalls":0}`,
			wantMessage: "positive integer",
		},
		{
			label:       "empty version",
			factoryName: apiproxy.PluginName,
			input:       `{"version":""}`,
			wantMessage: "version must be non-empty",
		},
		{
			label:       "credentials unknown",
			factoryName: credentials.PluginName,
			input:       `{"local":{"path":"/tmp/.credentials.json"},"unknown":true}`,
			wantMessage: "unknown field",
		},
		{
			label:       "credentials relative path",
			factoryName: credentials.PluginName,
			input:       `{"local":{"path":".credentials.json"}}`,
			wantMessage: "path must be absolute",
		},
		{
			label:       "prompt wrong type",
			factoryName: systemprompt.PluginName,
			input:       `{"includeHarnessIdentity":"yes"}`,
			wantMessage: "must be a boolean",
		},
		{
			label:       "tools code mode",
			factoryName: tools.PluginName,
			input:       `{"mode":"code"}`,
			wantMessage: "Code Runtime bridge",
		},
		{
			label:       "approval policy",
			factoryName: approval.PluginName,
			input:       `{"policy":"sometimes"}`,
			wantMessage: "must be ask or never",
		},
		{
			label:       "questions unknown",
			factoryName: userquestions.PluginName,
			input:       `{"unknown":true}`,
			wantMessage: "unknown field",
		},
		{
			label:       "ask tool unknown",
			factoryName: toolaskuser.PluginName,
			input:       `{"unknown":true}`,
			wantMessage: "unknown field",
		},
		{
			label:       "llm unknown",
			factoryName: llm.PluginName,
			input:       `{"unknown":true}`,
			wantMessage: "unknown field",
		},
		{
			label:       "llm retry policy",
			factoryName: llmretry.PluginName,
			input:       `{"retryPolicy":{"mode":"always"}}`,
			wantMessage: "belongs under each provider",
		},
		{
			label:       "llm retry null",
			factoryName: llmretry.PluginName,
			input:       `null`,
			wantMessage: "JSON object",
		},
		{
			label:       "projection unknown",
			factoryName: projection.PluginName,
			input:       `{"unknown":true}`,
			wantMessage: "unknown field",
		},
		{
			label:       "query empty path",
			factoryName: query.PluginName,
			input:       `{"path":""}`,
			wantMessage: "path must be non-empty",
		},
		{
			label:       "title invalid cap",
			factoryName: title.PluginName,
			input:       `{"fallbackMaxWords":5,"fallbackMaxBytes":81,"maxTitleBytes":80}`,
			wantMessage: "must not exceed",
		},
		{
			label:       "persistence journal",
			factoryName: persistence.PluginName,
			input:       `{"path":"/tmp/sessions.sqlite","journalMode":"memory"}`,
			wantMessage: "journalMode must be",
		},
		{
			label:       "persistence delay overflow",
			factoryName: persistence.PluginName,
			input:       `{"path":"/tmp/sessions.sqlite","writeBatchMaxDelayMs":9223372036854775807}`,
			wantMessage: "must not exceed",
		},
		{
			label:       "workspace journal",
			factoryName: workspace.PluginName,
			input:       `{"path":"/tmp/workspaces.sqlite","journalMode":"memory"}`,
			wantMessage: "journalMode must be",
		},
		{
			label:       "web unknown",
			factoryName: web.PluginName,
			input:       `{"unknown":true}`,
			wantMessage: "unknown field",
		},
		{
			label:       "deepseek nested unknown",
			factoryName: deepseek.PluginName,
			input:       `{"models":[{"id":"m","unknown":true}]}`,
			wantMessage: "unknown field",
		},
		{
			label:       "duplicate nested field",
			factoryName: credentials.PluginName,
			input:       `{"local":{"path":"/tmp/a","path":"/tmp/b"}}`,
			wantMessage: "duplicate field",
		},
		{
			label:       "dynamic syntax",
			factoryName: connectionhost.PluginName,
			input:       `!!js (() => ({}))`,
			wantMessage: "invalid configuration",
		},
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.label, func(t *testing.T) {
			t.Parallel()
			builder, err := directory.Lookup(testCase.factoryName)
			if err != nil {
				t.Fatal(err)
			}
			_, err = builder.Create(
				context.Background(),
				json.RawMessage(testCase.input),
			)
			if err == nil || !strings.Contains(err.Error(), testCase.wantMessage) {
				t.Fatalf(
					"Create error = %v, want containing %q",
					err,
					testCase.wantMessage,
				)
			}
		})
	}
}

func TestServerTreeStartsCapabilitiesBeforeExposingConnection(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	directory := newTestCatalog(t, "/contract-workspace")
	dataDirectory := t.TempDir()
	specs, err := DefaultSpecs(
		"127.0.0.1:0",
		"0.1.0-rc.5",
		dataDirectory+"/sessions.sqlite",
		dataDirectory+"/workspaces.sqlite",
	)
	if err != nil {
		t.Fatal(err)
	}
	probe, specs := addProbe(t, directory, specs)
	assembledServer, err := BuildServer(requestContext, directory, specs)
	if err != nil {
		t.Fatal(err)
	}
	if assembledServer.BoundAddress() != "" {
		t.Fatal("Connection bound before Runtime admission")
	}
	failureSink := testDiagnostics(t)
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{
		EventFailures: failureSink,
	})
	if _, err = runtimeEngine.Start(requestContext, assembledServer); err != nil {
		t.Fatal(err)
	}
	serverAddress := assembledServer.BoundAddress()
	if serverAddress == "" {
		t.Fatal("Connection did not bind after successful activation")
	}
	if liveAgents := probe.agents.List(); len(liveAgents) != 0 {
		t.Fatalf("default live Agents = %#v", liveAgents)
	}
	projectionSession, err := session.New(
		"assembly-subagent-projections",
		session.CreateOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	projectionSnapshot, err := probe.projections.Snapshot(projectionSession)
	if err != nil {
		t.Fatal(err)
	}
	if string(projectionSnapshot.Values["subagent"]) != "null" ||
		string(projectionSnapshot.Values["subagentTiming"]) != `{"settledMs":0}` {
		t.Fatalf("default Subagent projections = %#v", projectionSnapshot.Values)
	}
	if got := probe.defaultModel.CurrentSelection(); got.Provider != deepseek.ProviderRoute || got.Model != deepseek.DefaultModelID {
		t.Fatalf("default model = %#v", got)
	}
	assembledPrompt, err := probe.prompts.Assemble(
		requestContext,
		systemprompt.AssembleContext{},
	)
	if err != nil {
		t.Fatal(err)
	}
	promptText, err := systemprompt.RenderPrompt(assembledPrompt)
	if err != nil {
		t.Fatal(err)
	}
	wantPrompt := "You are an AI agent powered by DeepSeek Harness.\n\n" +
		"Use subagent in the background by default. Start independent delegations together and continue useful work while they run. Set `run_in_background: false` only when your next action depends on that subagent's result."
	if promptText != wantPrompt {
		t.Fatalf("default System Prompt = %q", promptText)
	}
	toolSchemas := probe.toolRuntime.Schemas()
	toolNames := make([]string, len(toolSchemas))
	for index, schema := range toolSchemas {
		toolNames[index] = schema.Name
	}
	wantToolNames := []string{
		toolaskuser.Name,
		subagentdelegation.DefaultToolName,
		"send_message",
		"interrupt_agent",
		"list_agents",
	}
	if !reflect.DeepEqual(toolNames, wantToolNames) {
		t.Fatalf("default Tool schemas = %#v", toolSchemas)
	}
	wantProviders := []llm.ProviderInfo{
		{
			ID:   deepseek.ProviderRoute,
			Name: "DeepSeek",
		},
	}
	if got := probe.models.ListProviders(); !reflect.DeepEqual(got, wantProviders) {
		t.Fatalf("default LLM providers = %#v", got)
	}
	handle, err := probe.constructor.Create(requestContext, agent.CreateOptions{
		SessionID: "assembly-agent",
		AgentOptions: agent.Options{
			Provider: deepseek.ProviderRoute,
			Model:    deepseek.DefaultModelID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, found := probe.agents.Get("assembly-agent"); !found {
		t.Fatal("Agent Loop did not publish the requested Agent")
	}
	if err = handle.Dispose(requestContext); err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"type":"client-request","rpcId":"assembly-1","method":"host.describe","payload":{}}`)
	httpRequest, err := http.NewRequestWithContext(
		requestContext,
		http.MethodPost,
		"http://"+serverAddress+protocol.APIPath+"/"+apiproxy.HostDescribeMethod,
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	httpRequest.Header.Set("content-type", "application/json")
	response, err := (&http.Client{
		Timeout: 2 * time.Second,
	}).Do(httpRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var message protocol.ServerResponse
	if err = json.NewDecoder(response.Body).Decode(&message); err != nil {
		t.Fatal(err)
	}
	var description apiproxy.HostDescription
	if err = json.Unmarshal(message.Result.Value, &description); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !message.Result.OK ||
		description.Version != "0.1.0-rc.5" ||
		description.CWD != "/contract-workspace" ||
		description.Provider != deepseek.ProviderRoute ||
		description.Model != deepseek.DefaultModelID ||
		description.AttachedSessions != 0 {
		t.Fatalf(
			"host.describe response = (%d, %#v, %#v)",
			response.StatusCode,
			message,
			description,
		)
	}
	if err = runtimeEngine.Shutdown(requestContext); err != nil {
		t.Fatal(err)
	}
}

func TestConnectionActivationFailureRollsBackWholeServer(t *testing.T) {
	t.Parallel()
	reservedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer reservedListener.Close()
	directory := newTestCatalog(t, t.TempDir())
	dataDirectory := t.TempDir()
	specs, err := DefaultSpecs(
		reservedListener.Addr().String(),
		"test",
		dataDirectory+"/sessions.sqlite",
		dataDirectory+"/workspaces.sqlite",
	)
	if err != nil {
		t.Fatal(err)
	}
	assembledServer, err := BuildServer(context.Background(), directory, specs)
	if err != nil {
		t.Fatal(err)
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{
		EventFailures: testDiagnostics(t),
	})
	if _, err = runtimeEngine.Start(context.Background(), assembledServer); err == nil {
		t.Fatal("composition with occupied listener succeeded")
	}
	if assembledServer.BoundAddress() != "" {
		t.Fatalf(
			"failed Connection retained address %q",
			assembledServer.BoundAddress(),
		)
	}
	if statuses := runtimeEngine.Statuses(); len(statuses) != 0 {
		t.Fatalf("rolled-back statuses = %#v", statuses)
	}
}

var _ pluginfactory.Factory = (*probeFactory)(nil)
