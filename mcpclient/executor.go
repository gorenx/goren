package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

type callerKey struct{}

type caller struct {
	sessionID session.SessionID
	callID    agentmessage.CallID
}

type executor struct {
	connection *Connection
	definition remoteTool
}

func (body *executor) Execute(arguments json.RawMessage, runContext tools.ToolRunContext) (json.RawMessage, error) {
	if body.definition.taskRequired {
		return nil, errors.New("mcpclient: this tool requires unsupported task execution")
	}
	if runContext.Execution.SessionID == "" || runContext.Execution.CallID == "" {
		return nil, errors.New("mcpclient: exact calling Agent and Tool call identity are required")
	}
	peer, work, finish, err := body.connection.beginTool(runContext.Context, body.definition)
	if err != nil {
		return nil, err
	}
	defer finish()
	work, cancel := context.WithTimeout(work,
		time.Duration(body.connection.settings.ElicitationTimeoutMs)*time.Millisecond,
	)
	defer cancel()
	work = context.WithValue(work, callerKey{}, caller{
		sessionID: runContext.Execution.SessionID,
		callID:    runContext.Execution.CallID,
	})
	rawResult, err := peer.session.callTool(work, body.definition.rawName, arguments)
	if work.Err() != nil {
		return nil, work.Err()
	}
	if err != nil {
		return nil, errors.New("mcpclient: tools/call failed")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(rawResult, &fields) != nil {
		return nil, errors.New("mcpclient: invalid tool result")
	}
	var blocks []json.RawMessage
	if json.Unmarshal(fields["content"], &blocks) != nil || blocks == nil {
		return nil, errors.New("mcpclient: tool result content must be an array")
	}
	var failed bool
	if len(fields["isError"]) != 0 && json.Unmarshal(fields["isError"], &failed) != nil {
		return nil, errors.New("mcpclient: invalid isError")
	}
	if failed {
		return nil, errors.New("mcpclient: remote tool reported an error")
	}
	value := map[string]json.RawMessage{"content": fields["content"]}
	if structured, exists := fields["structuredContent"]; exists {
		value["structuredContent"] = structured
	}
	return json.Marshal(value)
}
