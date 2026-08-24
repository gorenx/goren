package apiproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/commands"
	"github.com/gorenx/goren/connection"
)

const (
	CommandsListMethod    = "commands/list"
	CommandsExecuteMethod = "commands/execute"
)

// EncodedCommandImage is the strict source wire shape. The current /compact
// definition rejects every non-empty image batch before its handler runs.
type EncodedCommandImage struct {
	MediaType string  `json:"mediaType"`
	Data      string  `json:"data"`
	Name      *string `json:"name,omitempty"`
}

// CommandsListRequest addresses one Agent through the Typert Remote wrapper.
type CommandsListRequest struct {
	AgentID SessionID
}

// CommandsExecuteRequest carries the exact command line and required images
// array from the generated Remote descriptor.
type CommandsExecuteRequest struct {
	AgentID SessionID
	Line    string
	Images  []EncodedCommandImage
}

// CommandsAPI is the selected Commands Remote surface.
type CommandsAPI interface {
	List(context.Context, Request[CommandsListRequest]) (Outcome[[]commands.Descriptor], error)
	Execute(context.Context, Request[CommandsExecuteRequest]) (Outcome[*commands.Execution], error)
}

// CommandAgentResolver is the consumer-owned port for live/cold ordinary Agent
// resolution and the existing subagent ownership fence.
type CommandAgentResolver interface {
	ResolveOrdinaryAgent(context.Context, SessionID) (agent.Agent, *connection.RPCError)
}

// CommandsGateway translates selected Remote requests into Commands use cases.
type CommandsGateway struct {
	commands commands.Registry
	agents   CommandAgentResolver
}

// NewCommandsGateway creates the Commands inbound adapter.
func NewCommandsGateway(
	commandRegistry commands.Registry,
	agentResolver CommandAgentResolver,
) (*CommandsGateway, error) {
	if commandRegistry == nil || agentResolver == nil {
		return nil, errors.New("apiproxy: Commands dependencies are incomplete")
	}
	return &CommandsGateway{
		commands: commandRegistry,
		agents:   agentResolver,
	}, nil
}

// List returns the effective command directory for one ordinary Agent.
func (owner *CommandsGateway) List(
	requestContext context.Context,
	call Request[CommandsListRequest],
) (Outcome[[]commands.Descriptor], error) {
	subject, refused := owner.agents.ResolveOrdinaryAgent(
		requestContext,
		call.Payload.AgentID,
	)
	if refused != nil {
		return Fail[[]commands.Descriptor](*refused), nil
	}
	return OK(owner.commands.List(subject)), nil
}

// Execute resolves and invokes one command without scheduling a model turn.
func (owner *CommandsGateway) Execute(
	requestContext context.Context,
	call Request[CommandsExecuteRequest],
) (Outcome[*commands.Execution], error) {
	subject, refused := owner.agents.ResolveOrdinaryAgent(
		requestContext,
		call.Payload.AgentID,
	)
	if refused != nil {
		return Fail[*commands.Execution](*refused), nil
	}
	settled, err := owner.commands.Execute(
		requestContext,
		subject,
		call.Payload.Line,
		commands.ExecuteOptions{
			AttachmentCount: len(call.Payload.Images),
		},
	)
	if err != nil {
		return Outcome[*commands.Execution]{}, err
	}
	if settled == nil {
		return Absent[*commands.Execution](), nil
	}
	return OK(settled), nil
}

// RegisterCommandsAPI installs the two selected source Remote endpoints.
func RegisterCommandsAPI(methods *Catalog, commandMethods CommandsAPI) error {
	if commandMethods == nil {
		return errors.New("apiproxy: Commands API is nil")
	}
	if err := RegisterRemoteUnary(
		methods,
		CommandsListMethod,
		decodeCommandsListRemoteRequest,
		commandMethods.List,
	); err != nil {
		return err
	}
	return RegisterRemoteUnary(
		methods,
		CommandsExecuteMethod,
		decodeCommandsExecuteRemoteRequest,
		commandMethods.Execute,
	)
}

func decodeCommandsListRemoteRequest(
	rawPayload json.RawMessage,
) (CommandsListRequest, *connection.RPCError) {
	decoded, issues := DecodeCommandsListRequest(rawPayload)
	return decoded, commandRemoteBoundaryFailure(CommandsListMethod, issues)
}

func decodeCommandsExecuteRemoteRequest(
	rawPayload json.RawMessage,
) (CommandsExecuteRequest, *connection.RPCError) {
	decoded, issues := DecodeCommandsExecuteRequest(rawPayload)
	return decoded, commandRemoteBoundaryFailure(CommandsExecuteMethod, issues)
}

// DecodeCommandsListRequest validates the exact Typert {args:{agentId}} shape.
func DecodeCommandsListRequest(
	rawPayload json.RawMessage,
) (CommandsListRequest, []connection.ValidationIssue) {
	arguments, issues := decodeCommandRemoteArguments(rawPayload)
	if len(issues) != 0 {
		return CommandsListRequest{}, issues
	}
	if issues = exactCommandArgumentFields(arguments, "agentId"); len(issues) != 0 {
		return CommandsListRequest{}, issues
	}
	identifier, fieldIssues := commandStringField(arguments, "agentId")
	return CommandsListRequest{
		AgentID: SessionID(identifier),
	}, fieldIssues
}

// DecodeCommandsExecuteRequest validates the generated Commands descriptor.
func DecodeCommandsExecuteRequest(
	rawPayload json.RawMessage,
) (CommandsExecuteRequest, []connection.ValidationIssue) {
	arguments, issues := decodeCommandRemoteArguments(rawPayload)
	if len(issues) != 0 {
		return CommandsExecuteRequest{}, issues
	}
	if issues = exactCommandArgumentFields(arguments, "agentId", "line", "images"); len(issues) != 0 {
		return CommandsExecuteRequest{}, issues
	}
	identifier, identifierIssues := commandStringField(arguments, "agentId")
	line, lineIssues := commandStringField(arguments, "line")
	images, imageIssues := decodeCommandImages(arguments["images"])
	issues = append(issues, identifierIssues...)
	issues = append(issues, lineIssues...)
	issues = append(issues, imageIssues...)
	return CommandsExecuteRequest{
		AgentID: SessionID(identifier),
		Line:    line,
		Images:  images,
	}, issues
}

func decodeCommandRemoteArguments(
	rawPayload json.RawMessage,
) (map[string]json.RawMessage, []connection.ValidationIssue) {
	fields, valid := decodeCommandObject(rawPayload)
	if !valid || len(fields) != 1 || fields["args"] == nil {
		return nil, []connection.ValidationIssue{
			commandInvalidIssue(
				[]string{},
				"Remote payload must contain exactly one plain-object args field",
			),
		}
	}
	arguments, valid := decodeCommandObject(fields["args"])
	if !valid {
		return nil, []connection.ValidationIssue{
			commandInvalidIssue(
				[]string{},
				"Remote payload must contain exactly one plain-object args field",
			),
		}
	}
	return arguments, nil
}

func exactCommandArgumentFields(
	arguments map[string]json.RawMessage,
	expected ...string,
) []connection.ValidationIssue {
	expectedSet := make(map[string]struct{}, len(expected))
	missing := make([]string, 0)
	for _, fieldName := range expected {
		expectedSet[fieldName] = struct{}{}
		if arguments[fieldName] == nil {
			missing = append(missing, fieldName)
		}
	}
	unexpected := make([]string, 0)
	for fieldName := range arguments {
		if _, found := expectedSet[fieldName]; !found {
			unexpected = append(unexpected, fieldName)
		}
	}
	if len(missing) != 0 || len(unexpected) != 0 {
		sort.Strings(missing)
		sort.Strings(unexpected)
		clauses := make([]string, 0, 2)
		if len(missing) != 0 {
			clauses = append(clauses, "missing "+quoteCommandFields(missing))
		}
		if len(unexpected) != 0 {
			clauses = append(clauses, "unexpected "+quoteCommandFields(unexpected))
		}
		return []connection.ValidationIssue{
			commandInvalidIssue(
				[]string{"args"},
				"args fields do not match the descriptor: "+strings.Join(clauses, "; "),
			),
		}
	}
	return nil
}

func quoteCommandFields(fields []string) string {
	quoted := make([]string, len(fields))
	for fieldIndex, fieldName := range fields {
		quoted[fieldIndex] = fmt.Sprintf("%q", fieldName)
	}
	return strings.Join(quoted, ", ")
}

func commandStringField(
	arguments map[string]json.RawMessage,
	fieldName string,
) (string, []connection.ValidationIssue) {
	var textValue string
	if json.Unmarshal(arguments[fieldName], &textValue) != nil {
		return "", []connection.ValidationIssue{
			{
				Code:     "invalid_type",
				Expected: "string",
				Path:     []string{"args", fieldName},
				Message:  "Invalid input: expected string",
			},
		}
	}
	return textValue, nil
}

func decodeCommandImages(
	rawValue json.RawMessage,
) ([]EncodedCommandImage, []connection.ValidationIssue) {
	if bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) {
		return nil, []connection.ValidationIssue{
			{
				Code:     "invalid_type",
				Expected: "array",
				Path:     []string{"args", "images"},
				Message:  "Invalid input: expected array",
			},
		}
	}
	var rawImages []json.RawMessage
	if json.Unmarshal(rawValue, &rawImages) != nil {
		return nil, []connection.ValidationIssue{
			{
				Code:     "invalid_type",
				Expected: "array",
				Path:     []string{"args", "images"},
				Message:  "Invalid input: expected array",
			},
		}
	}
	images := make([]EncodedCommandImage, 0, len(rawImages))
	issues := make([]connection.ValidationIssue, 0)
	for imageIndex, rawImage := range rawImages {
		fields, valid := decodeCommandObject(rawImage)
		if !valid {
			issues = append(issues, commandInvalidIssue(
				[]string{"args", "images", jsonIndex(imageIndex)},
				"Invalid input: expected object",
			))
			continue
		}
		mediaType, mediaIssues := requiredStringField(fields, "mediaType", false)
		data, dataIssues := requiredStringField(fields, "data", false)
		imageName, nameIssues := optionalStringField(fields, "name", false)
		issues = append(
			issues,
			prefixIssues(mediaIssues, "args", "images", jsonIndex(imageIndex))...,
		)
		issues = append(
			issues,
			prefixIssues(dataIssues, "args", "images", jsonIndex(imageIndex))...,
		)
		issues = append(
			issues,
			prefixIssues(nameIssues, "args", "images", jsonIndex(imageIndex))...,
		)
		if len(mediaIssues) == 0 && !supportedCommandImageType(mediaType) {
			issues = append(issues, commandInvalidIssue(
				[]string{"args", "images", jsonIndex(imageIndex), "mediaType"},
				"Invalid image mediaType",
			))
		}
		if len(mediaIssues)+len(dataIssues)+len(nameIssues) == 0 &&
			supportedCommandImageType(mediaType) {
			images = append(images, EncodedCommandImage{
				MediaType: mediaType,
				Data:      data,
				Name:      imageName,
			})
		}
	}
	return images, issues
}

func decodeCommandObject(rawValue json.RawMessage) (map[string]json.RawMessage, bool) {
	if !isCommandJSONObject(rawValue) {
		return nil, false
	}
	fields := make(map[string]json.RawMessage)
	if json.Unmarshal(rawValue, &fields) != nil {
		return nil, false
	}
	return fields, true
}

func isCommandJSONObject(rawValue json.RawMessage) bool {
	trimmed := bytes.TrimSpace(rawValue)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}

func supportedCommandImageType(mediaType string) bool {
	switch mediaType {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}

func commandInvalidIssue(path []string, message string) connection.ValidationIssue {
	return connection.ValidationIssue{
		Code:    "invalid_value",
		Path:    path,
		Message: message,
	}
}

func commandRemoteBoundaryFailure(
	method string,
	issues []connection.ValidationIssue,
) *connection.RPCError {
	if len(issues) == 0 {
		return nil
	}
	message := issues[0].Message
	if len(issues[0].Path) == 0 {
		return remoteInternalFailure(message)
	}
	if len(issues[0].Path) == 1 && issues[0].Path[0] == "args" {
		return remoteInternalFailure(fmt.Sprintf(
			"typert gateway: %s: %s",
			method,
			message,
		))
	}
	fieldName := issues[0].Path[0]
	if fieldName == "args" && len(issues[0].Path) > 1 {
		fieldName = issues[0].Path[1]
	}
	return remoteInternalFailure(fmt.Sprintf(
		"typert gateway: %s: wire field %q failed boundary validation",
		method,
		fieldName,
	))
}

func remoteInternalFailure(message string) *connection.RPCError {
	problem := NewRPCError(
		connection.ErrorInternal,
		message,
		struct{}{},
	)
	return &problem
}
