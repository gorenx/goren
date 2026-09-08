package commandsapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	rpc "github.com/gorenx/goren/apiproxy/rpc"
	"sort"
	"strconv"
	"strings"

	"github.com/gorenx/goren/commands"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"
)

const (
	ListMethod    = "commands/list"
	ExecuteMethod = "commands/execute"
)

// Image is the strict source wire shape. The current /compact
// definition rejects every non-empty image batch before its handler runs.
type Image struct {
	MediaType string  `json:"mediaType"`
	Data      string  `json:"data"`
	Name      *string `json:"name,omitempty"`
}

// ListRequest addresses one Agent through the Typert Remote wrapper.
type ListRequest struct {
	AgentID session.SessionID
}

// ExecuteRequest carries the exact command line and required images
// array from the generated Remote descriptor.
type ExecuteRequest struct {
	AgentID session.SessionID
	Line    string
	Images  []Image
}

// API is the selected Commands Remote surface.
type API interface {
	List(context.Context, rpc.Request[ListRequest]) (rpc.Outcome[[]commands.Descriptor], error)
	Execute(context.Context, rpc.Request[ExecuteRequest]) (rpc.Outcome[*commands.Execution], error)
}

// Register installs the two selected source Remote endpoints.
func Register(methods *rpc.Catalog, commandMethods API) error {
	if commandMethods == nil {
		return errors.New("apiproxy: Commands API is nil")
	}
	if err := rpc.RegisterRemoteUnary(
		methods,
		ListMethod,
		decodeCommandsListRemoteRequest,
		commandMethods.List,
	); err != nil {
		return err
	}
	return rpc.RegisterRemoteUnary(
		methods,
		ExecuteMethod,
		decodeCommandsExecuteRemoteRequest,
		commandMethods.Execute,
	)
}

func decodeCommandsListRemoteRequest(
	rawPayload json.RawMessage,
) (ListRequest, *connection.RPCError) {
	decoded, issues := DecodeListRequest(rawPayload)
	return decoded, commandRemoteBoundaryFailure(ListMethod, issues)
}

func decodeCommandsExecuteRemoteRequest(
	rawPayload json.RawMessage,
) (ExecuteRequest, *connection.RPCError) {
	decoded, issues := DecodeExecuteRequest(rawPayload)
	return decoded, commandRemoteBoundaryFailure(ExecuteMethod, issues)
}

// DecodeListRequest validates the exact Typert {args:{agentId}} shape.
func DecodeListRequest(
	rawPayload json.RawMessage,
) (ListRequest, []connection.ValidationIssue) {
	arguments, issues := decodeCommandRemoteArguments(rawPayload)
	if len(issues) != 0 {
		return ListRequest{}, issues
	}
	if issues = exactCommandArgumentFields(arguments, "agentId"); len(issues) != 0 {
		return ListRequest{}, issues
	}
	identifier, fieldIssues := commandStringField(arguments, "agentId")
	return ListRequest{
		AgentID: session.SessionID(identifier),
	}, fieldIssues
}

// DecodeExecuteRequest validates the generated Commands descriptor.
func DecodeExecuteRequest(
	rawPayload json.RawMessage,
) (ExecuteRequest, []connection.ValidationIssue) {
	arguments, issues := decodeCommandRemoteArguments(rawPayload)
	if len(issues) != 0 {
		return ExecuteRequest{}, issues
	}
	if issues = exactCommandArgumentFields(arguments, "agentId", "line", "images"); len(issues) != 0 {
		return ExecuteRequest{}, issues
	}
	identifier, identifierIssues := commandStringField(arguments, "agentId")
	line, lineIssues := commandStringField(arguments, "line")
	images, imageIssues := decodeCommandImages(arguments["images"])
	issues = append(issues, identifierIssues...)
	issues = append(issues, lineIssues...)
	issues = append(issues, imageIssues...)
	return ExecuteRequest{
		AgentID: session.SessionID(identifier),
		Line:    line,
		Images:  images,
	}, issues
}

func decodeCommandRemoteArguments(
	rawPayload json.RawMessage,
) (map[string]json.RawMessage, []connection.ValidationIssue) {
	fields, valid := decodeCommandObject(rawPayload)
	if !valid ||
		len(fields) != 1 ||
		fields["args"] == nil {
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
) ([]Image, []connection.ValidationIssue) {
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
	images := make([]Image, 0, len(rawImages))
	issues := make([]connection.ValidationIssue, 0)
	for imageIndex, rawImage := range rawImages {
		fields, valid := decodeCommandObject(rawImage)
		if !valid {
			issues = append(issues, commandInvalidIssue(
				[]string{"args", "images", strconv.Itoa(imageIndex)},
				"Invalid input: expected object",
			))
			continue
		}
		mediaType, mediaIssues := rpc.RequiredString(fields, "mediaType", false)
		data, dataIssues := rpc.RequiredString(fields, "data", false)
		imageName, nameIssues := rpc.OptionalString(fields, "name", false)
		issues = append(
			issues,
			rpc.PrefixIssues(mediaIssues, "args", "images", strconv.Itoa(imageIndex))...,
		)
		issues = append(
			issues,
			rpc.PrefixIssues(dataIssues, "args", "images", strconv.Itoa(imageIndex))...,
		)
		issues = append(
			issues,
			rpc.PrefixIssues(nameIssues, "args", "images", strconv.Itoa(imageIndex))...,
		)
		if len(mediaIssues) == 0 && !supportedCommandImageType(mediaType) {
			issues = append(issues, commandInvalidIssue(
				[]string{"args", "images", strconv.Itoa(imageIndex), "mediaType"},
				"Invalid image mediaType",
			))
		}
		if len(mediaIssues)+len(dataIssues)+len(nameIssues) == 0 &&
			supportedCommandImageType(mediaType) {
			images = append(images, Image{
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
	problem := rpc.NewRPCError(
		connection.ErrorInternal,
		message,
		struct{}{},
	)
	return &problem
}
