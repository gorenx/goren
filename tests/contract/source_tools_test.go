//go:build contract

package contract_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
	toolscore "github.com/gorenx/goren/tools"
)

type toolsContractPlugin struct {
	ready func(toolscore.ToolRuntime, *plugin.Scope) error
}

func (toolsContractPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "tools-contract",
		Provides: []plugin.ServiceRef{
			systemprompt.Service.Ref(), toolscore.Service.Ref(),
		},
	}
}

func (instance toolsContractPlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		return err
	}
	promptService, err := systemprompt.New(requestContext, pluginScope, promptSettings)
	if err != nil {
		return err
	}
	toolSettings, err := toolscore.ValidateConfig(toolscore.Config{})
	if err != nil {
		return err
	}
	toolService, err := toolscore.New(requestContext, pluginScope, promptService, nil, nil, toolSettings)
	if err != nil {
		return err
	}
	if _, err := plugin.Provide(pluginScope, systemprompt.Service, promptService); err != nil {
		return err
	}
	if _, err := plugin.Provide(pluginScope, toolscore.Service, toolService); err != nil {
		return err
	}
	return instance.ready(toolService, pluginScope)
}

type toolResultObservation struct {
	IsError bool                   `json:"isError"`
	Value   json.RawMessage        `json:"value,omitempty"`
	Error   *toolscore.ToolFailure `json:"error,omitempty"`
	Content []llm.ContentBlock     `json:"content"`
}

type toolsContractObservation struct {
	GlobalSchemas       []llm.ToolSchema      `json:"globalSchemas"`
	ScopedSchemas       []llm.ToolSchema      `json:"scopedSchemas"`
	AfterDisposeSchemas []llm.ToolSchema      `json:"afterDisposeSchemas"`
	Success             toolResultObservation `json:"success"`
	Unknown             toolResultObservation `json:"unknown"`
	Denied              toolResultObservation `json:"denied"`
	Steps               []string              `json:"steps"`
}

func TestPinnedSourceNativeToolsMatchesGo(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	commandContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sourceOutput, err := runTypeScript(commandContext, sourceRoot,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "tools-native.ts"),
		sourceRoot, filepath.Join(repositoryRoot, "contracts", "deepseek-harness", "manifest.json"),
	)
	if err != nil {
		t.Fatal(err)
	}

	requestContext := context.Background()
	var toolService toolscore.ToolRuntime
	var providerScope *plugin.Scope
	engine := plugin.NewRuntime()
	if _, err := engine.Load(requestContext, toolsContractPlugin{
		ready: func(available toolscore.ToolRuntime, pluginScope *plugin.Scope) error {
			toolService = available
			providerScope = pluginScope
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	steps := make([]string, 0)
	if _, err := toolscore.OnPreExecute(providerScope,
		func(chainContext context.Context, _ toolscore.ToolExecution, downstream toolscore.PreExecuteNext) (toolscore.PreToolDecision, error) {
			steps = append(steps, "pre-before")
			decision, err := downstream(chainContext)
			steps = append(steps, "pre-after")
			return decision, err
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolService.Guard(providerScope, toolscore.ToolGuardFunc(
		func(toolscore.ToolExecution) (string, bool) {
			steps = append(steps, "guard")
			return "", false
		})); err != nil {
		t.Fatal(err)
	}
	if _, err := toolscore.OnExecute(providerScope,
		func(chainContext context.Context, _ toolscore.ToolExecution, downstream toolscore.ExecuteNext) (toolscore.ToolExecutionResult, error) {
			steps = append(steps, "execute-before")
			outcome, err := downstream(chainContext)
			steps = append(steps, "execute-after")
			return outcome, err
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolscore.OnPostExecute(providerScope,
		func(chainContext context.Context, _ toolscore.ToolExecution, _ toolscore.ToolResultSnapshot, downstream toolscore.PostExecuteNext) (toolscore.PostToolDecision, error) {
			steps = append(steps, "post-before")
			decision, err := downstream(chainContext)
			steps = append(steps, "post-after")
			return decision, err
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolscore.OnResult(providerScope,
		func(context.Context, toolscore.ToolExecution, toolscore.ToolResultSnapshot) error {
			steps = append(steps, "result")
			return nil
		}); err != nil {
		t.Fatal(err)
	}

	for _, definition := range []toolscore.ToolDefinition{
		contractTool("alpha", "global alpha", &steps),
		contractTool("beta", "global beta", &steps),
		contractTool("pipeline", "pipeline", &steps),
	} {
		if _, err := toolService.Register(requestContext, providerScope, definition); err != nil {
			t.Fatal(err)
		}
	}
	success := toolService.Execute(requestContext, toolscore.ToolExecutionInput{
		CallID: "call-1", Name: "pipeline", Arguments: json.RawMessage(`{"value":"ok"}`),
	})
	unknown := toolService.Execute(requestContext, toolscore.ToolExecutionInput{
		CallID: "unknown-1", Name: "absent", Arguments: json.RawMessage(`{}`),
	})
	childScope, childRelease, err := providerScope.Child("tools-contract-child")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := toolService.Restrict(requestContext, childScope,
		toolscore.ToolRestriction{Allow: []string{"alpha", "pipeline"}}); err != nil {
		t.Fatal(err)
	}
	for _, definition := range []toolscore.ToolDefinition{
		contractTool("beta", "scoped beta", &steps),
		contractTool("gamma", "scoped gamma", &steps),
	} {
		if _, err := toolService.Register(requestContext, childScope, definition); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := toolscore.OnPreExecute(childScope,
		func(chainContext context.Context, execution toolscore.ToolExecution, downstream toolscore.PreExecuteNext) (toolscore.PreToolDecision, error) {
			if execution.Name == "alpha" {
				return toolscore.DenyDecision{Reason: "policy denied"}, nil
			}
			return downstream(chainContext)
		}); err != nil {
		t.Fatal(err)
	}
	scopedSchemas := toolService.Schemas(childScope.Target())
	denied := toolService.Execute(requestContext, toolscore.ToolExecutionInput{
		CallID: "deny-1", Name: "alpha", Arguments: json.RawMessage(`{}`), Scope: childScope.Target(),
	})
	retainedKey := childScope.Target()
	if err := childRelease(requestContext); err != nil {
		t.Fatal(err)
	}
	assembled := toolsContractObservation{
		GlobalSchemas: toolService.Schemas(plugin.ScopeKey{}), ScopedSchemas: scopedSchemas,
		AfterDisposeSchemas: toolService.Schemas(retainedKey),
		Success:             observeToolResult(t, success), Unknown: observeToolResult(t, unknown),
		Denied: observeToolResult(t, denied), Steps: steps,
	}
	goOutput, err := json.Marshal(assembled)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Shutdown(requestContext); err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, goOutput, sourceOutput)
}

func contractTool(name string, description string, steps *[]string) toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name: name, Description: description,
		Parameters: json.RawMessage(`{"type":"object"}`),
		Output: toolscore.ToolOutputDefinition{
			Schema: json.RawMessage(`{"type":"object"}`),
			Renderer: toolscore.OutputRendererFunc(
				func(_ json.RawMessage, value json.RawMessage) ([]llm.ContentBlock, error) {
					if name == "pipeline" {
						*steps = append(*steps, "render")
					}
					return []llm.ContentBlock{llm.NewTextBlock(string(value))}, nil
				}),
		},
		Executor: toolscore.ExecutorFunc(
			func(arguments json.RawMessage, _ toolscore.ToolRunContext) (json.RawMessage, error) {
				if name == "pipeline" {
					*steps = append(*steps, "body")
				}
				return arguments, nil
			}),
	}
}

func observeToolResult(t *testing.T, outcome toolscore.ToolExecutionResult) toolResultObservation {
	t.Helper()
	retained := toolResultObservation{IsError: outcome.Failed(), Content: outcome.ContentBlocks()}
	switch selected := outcome.(type) {
	case *toolscore.ToolExecutionSuccess:
		retained.Value = append(json.RawMessage(nil), selected.Value...)
	case *toolscore.ToolExecutionFailure:
		detail := selected.Error
		retained.Error = &detail
	default:
		t.Fatalf("tool outcome type = %T", outcome)
	}
	return retained
}
