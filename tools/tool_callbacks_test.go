package tools_test

import (
	"encoding/json"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/tools"
)

type executorStub func(json.RawMessage, tools.ToolRunContext) (json.RawMessage, error)

func (operation executorStub) Execute(arguments json.RawMessage, runContext tools.ToolRunContext) (json.RawMessage, error) {
	return operation(arguments, runContext)
}

type outputRendererStub func(json.RawMessage, json.RawMessage) ([]agentmessage.ContentBlock, error)

func (operation outputRendererStub) Render(arguments json.RawMessage, value json.RawMessage) ([]agentmessage.ContentBlock, error) {
	return operation(arguments, value)
}

type contentFinalizerStub func(tools.ToolExecution, tools.ToolResultSnapshot) ([]agentmessage.ContentBlock, bool)

func (operation contentFinalizerStub) Finalize(toolCall tools.ToolExecution, resultView tools.ToolResultSnapshot) ([]agentmessage.ContentBlock, bool) {
	return operation(toolCall, resultView)
}

type concurrencyClassifierStub func(json.RawMessage) bool

func (operation concurrencyClassifierStub) ConcurrencySafe(arguments json.RawMessage) bool {
	return operation(arguments)
}
