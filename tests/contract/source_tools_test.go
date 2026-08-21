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

type contractWaterfallPlugin[
	I plugin.WaterfallInput,
	O plugin.WaterfallOutput,
] struct {
	plugin.Base
	name       string
	middleware plugin.WaterfallMiddleware[I, O]
}

func (owner *contractWaterfallPlugin[I, O]) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: owner.name,
		Waterfalls: []plugin.WaterfallMiddlewareBinding{
			plugin.WaterfallOf[I, O](owner),
		},
	}
}

func (*contractWaterfallPlugin[I, O]) Apply(context.Context) error {
	return nil
}

func (*contractWaterfallPlugin[I, O]) Dispose(context.Context) error {
	return nil
}

func (owner *contractWaterfallPlugin[I, O]) Intercept(
	requestContext context.Context,
	input I,
	downstream plugin.WaterfallAction[I, O],
) (O, error) {
	return owner.middleware.Intercept(requestContext, input, downstream)
}

type contractWaterfallFunc[
	I plugin.WaterfallInput,
	O plugin.WaterfallOutput,
] func(context.Context, I, plugin.WaterfallAction[I, O]) (O, error)

func (operation contractWaterfallFunc[I, O]) Intercept(
	requestContext context.Context,
	input I,
	downstream plugin.WaterfallAction[I, O],
) (O, error) {
	return operation(requestContext, input, downstream)
}

type contractToolResultObserver struct {
	plugin.Base
	steps *[]string
}

func (*contractToolResultObserver) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "tools-contract-result-observer",
		Events: []plugin.EventSubscription{
			plugin.EventOf[toolscore.ExecutionCompleted](),
		},
	}
}

func (*contractToolResultObserver) Apply(context.Context) error {
	return nil
}

func (*contractToolResultObserver) Dispose(context.Context) error {
	return nil
}

func (observer *contractToolResultObserver) ObserveEvent(
	_ context.Context,
	fact plugin.Event,
) error {
	if _, matches := fact.(toolscore.ExecutionCompleted); matches {
		*observer.steps = append(*observer.steps, "result")
	}
	return nil
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
	commandContext, cancel := context.WithTimeout(
		context.Background(),
		30*time.Second,
	)
	defer cancel()
	sourceOutput, err := runTypeScript(
		commandContext,
		sourceRoot,
		filepath.Join(
			repositoryRoot,
			"tests",
			"contract",
			"typescript",
			"tools-native.ts",
		),
		sourceRoot,
		filepath.Join(
			repositoryRoot,
			"contracts",
			"deepseek-harness",
			"manifest.json",
		),
	)
	if err != nil {
		t.Fatal(err)
	}

	requestContext := context.Background()
	steps := make([]string, 0)
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		t.Fatal(err)
	}
	toolSettings, err := toolscore.ValidateConfig(toolscore.Config{})
	if err != nil {
		t.Fatal(err)
	}
	promptService := systemprompt.New(
		promptSettings,
		systemprompt.RegistryOptions{},
	)
	toolService := toolscore.New(toolSettings)
	preMiddleware := &contractWaterfallPlugin[
		toolscore.PreExecuteRequest,
		toolscore.PreExecuteOutcome,
	]{
		name: "tools-contract-pre",
		middleware: contractWaterfallFunc[
			toolscore.PreExecuteRequest,
			toolscore.PreExecuteOutcome,
		](func(
			chainContext context.Context,
			input toolscore.PreExecuteRequest,
			downstream plugin.WaterfallAction[
				toolscore.PreExecuteRequest,
				toolscore.PreExecuteOutcome,
			],
		) (toolscore.PreExecuteOutcome, error) {
			steps = append(steps, "pre-before")
			outcome, chainErr := downstream.Execute(chainContext, input)
			steps = append(steps, "pre-after")
			return outcome, chainErr
		}),
	}
	executeMiddleware := &contractWaterfallPlugin[
		toolscore.ExecuteRequest,
		toolscore.ExecuteOutcome,
	]{
		name: "tools-contract-execute",
		middleware: contractWaterfallFunc[
			toolscore.ExecuteRequest,
			toolscore.ExecuteOutcome,
		](func(
			chainContext context.Context,
			input toolscore.ExecuteRequest,
			downstream plugin.WaterfallAction[
				toolscore.ExecuteRequest,
				toolscore.ExecuteOutcome,
			],
		) (toolscore.ExecuteOutcome, error) {
			steps = append(steps, "execute-before")
			outcome, chainErr := downstream.Execute(chainContext, input)
			steps = append(steps, "execute-after")
			return outcome, chainErr
		}),
	}
	postMiddleware := &contractWaterfallPlugin[
		toolscore.PostExecuteRequest,
		toolscore.PostExecuteOutcome,
	]{
		name: "tools-contract-post",
		middleware: contractWaterfallFunc[
			toolscore.PostExecuteRequest,
			toolscore.PostExecuteOutcome,
		](func(
			chainContext context.Context,
			input toolscore.PostExecuteRequest,
			downstream plugin.WaterfallAction[
				toolscore.PostExecuteRequest,
				toolscore.PostExecuteOutcome,
			],
		) (toolscore.PostExecuteOutcome, error) {
			steps = append(steps, "post-before")
			outcome, chainErr := downstream.Execute(chainContext, input)
			steps = append(steps, "post-after")
			return outcome, chainErr
		}),
	}
	runtimeEngine := newContractRuntime(t)
	handles, err := runtimeEngine.Start(
		requestContext,
		promptService,
		toolService,
		preMiddleware,
		executeMiddleware,
		postMiddleware,
		&contractToolResultObserver{
			steps: &steps,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if shutdownErr := runtimeEngine.Shutdown(requestContext); shutdownErr != nil {
			t.Error(shutdownErr)
		}
	}()
	if _, err = toolService.AddGuard(
		requestContext,
		"contract-guard",
		toolscore.ToolGuardFunc(func(toolscore.ToolExecution) (string, bool) {
			steps = append(steps, "guard")
			return "", false
		}),
	); err != nil {
		t.Fatal(err)
	}
	for _, definition := range []toolscore.ToolDefinition{
		contractTool("alpha", "global alpha", &steps),
		contractTool("beta", "global beta", &steps),
		contractTool("pipeline", "pipeline", &steps),
	} {
		if _, err = toolService.AddTool(requestContext, definition); err != nil {
			t.Fatal(err)
		}
	}
	success := toolService.Execute(
		requestContext,
		toolscore.ToolExecutionInput{
			CallID:    "call-1",
			Name:      "pipeline",
			Arguments: json.RawMessage(`{"value":"ok"}`),
		},
	)
	unknown := toolService.Execute(
		requestContext,
		toolscore.ToolExecutionInput{
			CallID:    "unknown-1",
			Name:      "absent",
			Arguments: json.RawMessage(`{}`),
		},
	)
	promptOverlay := systemprompt.NewOverlay(systemprompt.RegistryOptions{})
	promptOverlayHandle, err := runtimeEngine.MountScopedChild(
		requestContext,
		handles[0],
		promptOverlay,
	)
	if err != nil {
		t.Fatal(err)
	}
	toolOverlay := toolscore.NewOverlay()
	toolOverlayHandle, err := runtimeEngine.MountChild(
		requestContext,
		promptOverlayHandle,
		toolOverlay,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = toolOverlay.AddRestriction(
		requestContext,
		"contract-visible",
		toolscore.ToolRestriction{
			Allow: []string{
				"alpha",
				"pipeline",
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	for _, definition := range []toolscore.ToolDefinition{
		contractTool("beta", "scoped beta", &steps),
		contractTool("gamma", "scoped gamma", &steps),
	} {
		if _, err = toolOverlay.AddTool(requestContext, definition); err != nil {
			t.Fatal(err)
		}
	}
	denyMiddleware := &contractWaterfallPlugin[
		toolscore.PreExecuteRequest,
		toolscore.PreExecuteOutcome,
	]{
		name: "tools-contract-scoped-deny",
		middleware: contractWaterfallFunc[
			toolscore.PreExecuteRequest,
			toolscore.PreExecuteOutcome,
		](func(
			chainContext context.Context,
			input toolscore.PreExecuteRequest,
			downstream plugin.WaterfallAction[
				toolscore.PreExecuteRequest,
				toolscore.PreExecuteOutcome,
			],
		) (toolscore.PreExecuteOutcome, error) {
			if input.Execution().Name == "alpha" {
				return toolscore.PreExecuteOutcome{
					Decision: toolscore.DenyDecision{
						Reason: "policy denied",
					},
				}, nil
			}
			return downstream.Execute(chainContext, input)
		}),
	}
	if _, err = runtimeEngine.MountChild(
		requestContext,
		toolOverlayHandle,
		denyMiddleware,
	); err != nil {
		t.Fatal(err)
	}
	scopedSchemas := toolOverlay.Schemas()
	denied := toolOverlay.Execute(
		requestContext,
		toolscore.ToolExecutionInput{
			CallID:    "deny-1",
			Name:      "alpha",
			Arguments: json.RawMessage(`{}`),
		},
	)
	if err = runtimeEngine.Unload(requestContext, promptOverlayHandle); err != nil {
		t.Fatal(err)
	}
	assembled := toolsContractObservation{
		GlobalSchemas:       toolService.Schemas(),
		ScopedSchemas:       scopedSchemas,
		AfterDisposeSchemas: toolService.Schemas(),
		Success:             observeToolResult(t, success),
		Unknown:             observeToolResult(t, unknown),
		Denied:              observeToolResult(t, denied),
		Steps:               steps,
	}
	goOutput, err := json.Marshal(assembled)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, goOutput, sourceOutput)
}

func contractTool(
	name string,
	description string,
	steps *[]string,
) toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        name,
		Description: description,
		Parameters:  json.RawMessage(`{"type":"object"}`),
		Output: toolscore.ToolOutputDefinition{
			Schema: json.RawMessage(`{"type":"object"}`),
			Renderer: toolscore.OutputRendererFunc(func(
				_ json.RawMessage,
				value json.RawMessage,
			) ([]llm.ContentBlock, error) {
				if name == "pipeline" {
					*steps = append(*steps, "render")
				}
				return []llm.ContentBlock{
					llm.NewTextBlock(string(value)),
				}, nil
			}),
		},
		Executor: toolscore.ExecutorFunc(func(
			arguments json.RawMessage,
			_ toolscore.ToolRunContext,
		) (json.RawMessage, error) {
			if name == "pipeline" {
				*steps = append(*steps, "body")
			}
			return arguments, nil
		}),
	}
}

func observeToolResult(
	t *testing.T,
	outcome toolscore.ToolExecutionResult,
) toolResultObservation {
	t.Helper()
	retained := toolResultObservation{
		IsError: outcome.Failed(),
		Content: outcome.ContentBlocks(),
	}
	switch selected := outcome.(type) {
	case *toolscore.ToolExecutionSuccess:
		retained.Value = append(json.RawMessage(nil), selected.Value...)
	case *toolscore.ToolExecutionFailure:
		detail := selected.Error
		retained.Error = &detail
	default:
		t.Fatalf("Tool outcome type = %T", outcome)
	}
	return retained
}
