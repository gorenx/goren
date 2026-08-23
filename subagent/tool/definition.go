package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/tools"
)

var foregroundOutputSchema = json.RawMessage(`{
  "oneOf": [
    {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "kind": {"type": "string", "const": "continuable"},
        "subagentId": {"type": "string"}
      },
      "required": ["kind", "subagentId"]
    },
    {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "kind": {"type": "string", "const": "foreground"},
        "runId": {"type": "string"},
        "output": {"type": "array", "items": {}}
      },
      "required": ["kind", "runId", "output"]
    }
  ]
}`)

type arguments struct {
	Description     string `json:"description"`
	Prompt          string `json:"prompt"`
	RunInBackground *bool  `json:"run_in_background,omitempty"`
}

func (owner *Plugin) definition(
	provider subagent.Provider,
) tools.ToolDefinition {
	description, promptDescription := providerWording(
		provider.InheritsParentContext(),
	)
	description += owner.schedulingWording()
	return tools.ToolDefinition{
		Name:        owner.settings.ToolName,
		Description: description,
		Parameters:  owner.parameterSchema(promptDescription),
		Output: tools.ToolOutputDefinition{
			Schema:   foregroundOutputSchema,
			Renderer: tools.OutputRendererFunc(renderOutput),
		},
		Executor: tools.ExecutorFunc(owner.execute),
		ConcurrencyBehavior: tools.ConcurrencyClassifierFunc(func(
			json.RawMessage,
		) bool {
			return true
		}),
	}
}

func (owner *Plugin) parameterSchema(
	promptDescription string,
) json.RawMessage {
	properties := map[string]any{
		"description": map[string]any{
			"type":        "string",
			"description": "A short (3-5 word) description of the delegated task, for display.",
		},
		"prompt": map[string]any{
			"type":        "string",
			"description": promptDescription,
		},
	}
	if owner.settings.EnableRunInBackground {
		backgroundDescription := "Whether to run as a background job. This build does not include Jobs, so true is rejected. Defaults to false."
		if owner.settings.BackgroundMode == BackgroundContinuable {
			backgroundDescription = "Whether to return a durable continuable subagent id immediately. Defaults to true; set false to wait for the result."
		}
		properties["run_in_background"] = map[string]any{
			"type":        "boolean",
			"description": backgroundDescription,
		}
	}
	rawValue, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
		"required": []string{
			"description",
			"prompt",
		},
	})
	return rawValue
}

func (owner *Plugin) execute(
	rawArguments json.RawMessage,
	runContext tools.ToolRunContext,
) (json.RawMessage, error) {
	parentAgent, matches := runContext.Execution.Subject.(agent.Agent)
	if !matches || parentAgent == nil {
		return nil, errors.New("subagent tool requires a calling Agent")
	}
	var request arguments
	if decodeErr := json.Unmarshal(rawArguments, &request); decodeErr != nil {
		return nil, decodeErr
	}
	runInBackground, resolveErr := owner.resolveBackground(request)
	if resolveErr != nil {
		return nil, resolveErr
	}
	prompt := []llm.ContentBlock{
		llm.NewTextBlock(request.Prompt),
	}
	if runInBackground {
		started, startErr := owner.continuations.StartContinuable(
			runContext.Context,
			subagent.ContinuableStartSpec{
				Provider: owner.settings.Provider,
				Label:    request.Description,
				Request: subagent.ContinuableRequest{
					Prompt:       prompt,
					Parent:       parentAgent,
					AgentOptions: owner.settings.AgentOptions,
					MaxDepth:     owner.settings.MaxDepth,
					ToolFilter:   owner.settings.ToolFilter,
					Persona:      owner.settings.Persona,
				},
			},
		)
		if startErr != nil {
			return nil, startErr
		}
		return json.Marshal(struct {
			Kind       string `json:"kind"`
			SubagentID string `json:"subagentId"`
		}{
			Kind:       "continuable",
			SubagentID: string(started.ChildID),
		})
	}
	runHandle, startErr := owner.oneShots.Start(
		runContext.Context,
		owner.settings.Provider,
		subagent.StartRequest{
			Label:        stringPointer(request.Description),
			Prompt:       prompt,
			Parent:       parentAgent,
			AgentOptions: owner.settings.AgentOptions,
			MaxDepth:     owner.settings.MaxDepth,
			ToolFilter:   owner.settings.ToolFilter,
			Persona:      owner.settings.Persona,
		},
	)
	if startErr != nil {
		return nil, startErr
	}
	result, resultErr := runHandle.AwaitResult(runContext.Context)
	disposeErr := runHandle.Dispose(runContext.Context)
	if resultErr != nil || disposeErr != nil {
		return nil, errors.Join(resultErr, disposeErr)
	}
	if stopErr := failedResult(result); stopErr != nil {
		return nil, stopErr
	}
	return json.Marshal(struct {
		Kind   string             `json:"kind"`
		RunID  string             `json:"runId"`
		Output []llm.ContentBlock `json:"output"`
	}{
		Kind:   "foreground",
		RunID:  string(runHandle.ID()),
		Output: result.Output,
	})
}

func (owner *Plugin) resolveBackground(request arguments) (bool, error) {
	if !owner.settings.EnableRunInBackground {
		if request.RunInBackground != nil && *request.RunInBackground {
			return false, errors.New(
				"run_in_background is disabled for this subagent Tool",
			)
		}
		return false, nil
	}
	runInBackground := owner.settings.BackgroundMode == BackgroundContinuable
	if request.RunInBackground != nil {
		runInBackground = *request.RunInBackground
	}
	if runInBackground && owner.settings.BackgroundMode == BackgroundOneShot {
		return false, errors.New(
			"background one-shot subagents require Jobs, which is not included in this build",
		)
	}
	return runInBackground, nil
}

func failedResult(result subagent.Result) error {
	var headline string
	switch result.StopReason {
	case subagent.StopCompleted:
		return nil
	case subagent.StopAborted:
		headline = "subagent run was cancelled"
	case subagent.StopError:
		headline = "subagent run failed"
	case subagent.StopMaxTokens:
		headline = "subagent run hit its token limit before finishing"
	case subagent.StopRefusal:
		headline = "subagent declined the task"
	default:
		headline = fmt.Sprintf(
			"subagent run ended abnormally (%s)",
			result.StopReason,
		)
	}
	if result.Diagnostic != nil {
		headline += "\nDiagnostic: " + *result.Diagnostic
	}
	partial := visibleText(result.Output)
	if partial != "" {
		headline += "\nPartial output before the run ended:\n" + partial
	}
	return errors.New(headline)
}

func renderOutput(
	_ json.RawMessage,
	rawValue json.RawMessage,
) ([]llm.ContentBlock, error) {
	var value struct {
		Kind       string          `json:"kind"`
		SubagentID string          `json:"subagentId"`
		Output     json.RawMessage `json:"output"`
	}
	if decodeErr := json.Unmarshal(rawValue, &value); decodeErr != nil {
		return nil, decodeErr
	}
	if value.Kind == "continuable" {
		return []llm.ContentBlock{
			llm.NewTextBlock("started subagent " + value.SubagentID),
		}, nil
	}
	blocks, decodeErr := llm.DecodeContentBlocks(value.Output)
	if decodeErr != nil {
		return nil, decodeErr
	}
	return []llm.ContentBlock{
		llm.NewTextBlock(visibleText(blocks)),
	}, nil
}

func visibleText(content []llm.ContentBlock) string {
	var builder strings.Builder
	for _, block := range content {
		plainText, matches := block.(llm.PlainTextContent)
		if !matches {
			continue
		}
		textValue, available := plainText.PlainText()
		if available {
			builder.WriteString(textValue)
		}
	}
	return builder.String()
}

func providerWording(inheritsConversation bool) (string, string) {
	if inheritsConversation {
		return "Delegate a task to a subagent that inherits this conversation's completed turns. You receive its result, not its intermediate steps.", "The task for the subagent. It already sees this conversation's completed turns, so state only what is new."
	}
	return "Delegate a self-contained task to a separate subagent working in its own context. You receive its result, not its intermediate steps.", "The complete, self-contained task for the subagent. It does not share this conversation's context."
}

func (owner *Plugin) schedulingWording() string {
	if !owner.settings.EnableRunInBackground {
		return " This call waits for the subagent and returns its result."
	}
	if owner.settings.BackgroundMode == BackgroundContinuable {
		return " This tool runs in the background by default and immediately returns a durable subagent id. Set run_in_background to false to wait for the result."
	}
	return " This call waits for the result. Background one-shot execution is unavailable because this build does not include Jobs."
}

func stringPointer(value string) *string {
	return &value
}
