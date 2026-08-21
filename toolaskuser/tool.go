// Package toolaskuser adapts the User Questions capability into the canonical
// model-facing ask_user_question Tool.
package toolaskuser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/userquestions"
)

const (
	// PluginName is the canonical Harness ask-user Tool Plugin name.
	PluginName = "@deepseek-ai/dsh-tool-ask-user"
	// Name is the canonical model-facing Tool name.
	Name = "ask_user_question"
	// Description is the exact model-facing Tool description from the pinned source.
	Description = "Ask the user a concise question when you need confirmation, a choice, or missing information before proceeding. " +
		"Send one or more questions, each with a stable id that will be echoed in the answer."
)

var (
	parameterSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "questions": {
      "type": "array",
      "description": "Questions to ask the user before continuing.",
      "items": {
        "type": "object",
        "additionalProperties": true,
        "properties": {
          "id": {"type": "string", "description": "Stable id for this question; echoed in the answer."},
          "question": {"type": "string", "description": "The specific question to ask the user."},
          "header": {"type": "string", "description": "Optional short heading for the question, such as \"Confirm\" or \"Choose Mode\"."},
          "options": {
            "type": "array",
            "description": "Optional choices to show the user. If you recommend one, put it first and append \"(Recommended)\" to that label.",
            "items": {
              "type": "object",
              "additionalProperties": true,
              "properties": {
                "label": {"type": "string", "description": "Short user-facing option label."},
                "description": {"type": "string", "description": "One sentence explaining the tradeoff or impact."}
              },
              "required": ["label"]
            }
          },
          "multi_select": {"type": "boolean", "description": "Whether the user may select more than one option. Defaults to false."}
        },
        "required": ["id", "question"]
      }
    }
  },
  "required": ["questions"]
}`)
	outputSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "answers": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "id": {"type": "string"},
          "selected": {"type": "array", "items": {"type": "string"}},
          "custom": {"type": "string"}
        },
        "required": ["id", "selected"]
      }
    }
  },
  "required": ["answers"]
}`)
)

type input struct {
	Questions []inputQuestion `json:"questions"`
}

type inputQuestion struct {
	ID          string                  `json:"id"`
	Question    string                  `json:"question"`
	Header      *string                 `json:"header,omitempty"`
	Options     *[]userquestions.Option `json:"options,omitempty"`
	MultiSelect *bool                   `json:"multi_select,omitempty"`
}

// Plugin owns the ask_user_question definition in the active Tool Catalog.
type Plugin struct {
	plugin.Base
	tool *tools.ToolHandle
}

// New constructs an inactive ask-user Tool Plugin.
func New() *Plugin {
	return &Plugin{}
}

// Manifest declares the Tool Catalog and User Questions dependencies.
func (*Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[tools.ToolCatalog](),
			plugin.ServiceOf[userquestions.UserQuestions](),
		},
	}
}

// Apply constructs and installs the Tool definition owned by this Plugin.
func (owner *Plugin) Apply(requestContext context.Context) error {
	toolCatalog, err := plugin.Require[tools.ToolCatalog](owner)
	if err != nil {
		return err
	}
	questionService, err := plugin.Require[userquestions.UserQuestions](owner)
	if err != nil {
		return err
	}
	definition, err := newDefinition(questionService)
	if err != nil {
		return err
	}
	toolHandle, err := toolCatalog.AddTool(requestContext, definition)
	if err != nil {
		return err
	}
	owner.tool = toolHandle
	return nil
}

// Dispose removes only the Tool definition installed by this Plugin.
func (owner *Plugin) Dispose(closeContext context.Context) error {
	if owner.tool == nil {
		return nil
	}
	disposeErr := owner.tool.Unregister(closeContext)
	owner.tool = nil
	return disposeErr
}

func newDefinition(
	questionService userquestions.UserQuestions,
) (tools.ToolDefinition, error) {
	if questionService == nil {
		return tools.ToolDefinition{}, errors.New("toolaskuser: User Questions service is nil")
	}
	return tools.ToolDefinition{
		Name:        Name,
		Description: Description,
		Parameters:  append(json.RawMessage(nil), parameterSchema...),
		Output: tools.ToolOutputDefinition{
			Schema: append(json.RawMessage(nil), outputSchema...),
			Renderer: tools.OutputRendererFunc(func(_ json.RawMessage, value json.RawMessage) ([]llm.ContentBlock, error) {
				var compact bytes.Buffer
				if err := json.Compact(&compact, value); err != nil {
					return nil, fmt.Errorf("toolaskuser: render output: %w", err)
				}
				return []llm.ContentBlock{llm.NewTextBlock(compact.String())}, nil
			}),
		},
		Executor: tools.ExecutorFunc(
			func(arguments json.RawMessage, runContext tools.ToolRunContext) (json.RawMessage, error) {
				var decoded input
				if err := json.Unmarshal(arguments, &decoded); err != nil {
					return nil, fmt.Errorf("toolaskuser: decode validated arguments: %w", err)
				}
				questions := make([]userquestions.Question, len(decoded.Questions))
				for index, item := range decoded.Questions {
					questions[index] = userquestions.Question{
						ID:          item.ID,
						Question:    item.Question,
						Header:      cloneString(item.Header),
						Options:     cloneOptions(item.Options),
						MultiSelect: cloneBool(item.MultiSelect),
					}
				}
				var subject agent.Agent
				if runContext.Execution.Subject != nil {
					var matches bool
					subject, matches = runContext.Execution.Subject.(agent.Agent)
					if !matches {
						return nil, errors.New(
							"toolaskuser: execution subject is not an Agent",
						)
					}
				}
				answerValue, err := questionService.Ask(runContext.Context, userquestions.Request{
					Questions: questions,
					Subject:   subject,
				})
				if err != nil {
					return nil, err
				}
				encoded, err := json.Marshal(answerValue)
				if err != nil {
					return nil, fmt.Errorf("toolaskuser: encode answer: %w", err)
				}
				return encoded, nil
			}),
	}, nil
}

func cloneOptions(source *[]userquestions.Option) *[]userquestions.Option {
	if source == nil {
		return nil
	}
	detached := make([]userquestions.Option, len(*source))
	for index, item := range *source {
		detached[index] = item
		detached[index].Description = cloneString(item.Description)
	}
	return &detached
}

func cloneString(source *string) *string {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}

func cloneBool(source *bool) *bool {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}
