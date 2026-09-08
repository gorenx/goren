package workspaceapi

import (
	"encoding/json"

	sessionframes "github.com/gorenx/goren/apiproxy/session/events"

	"strings"

	rpc "github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/connection"
)

// DecodeListRequest validates the empty-object baseline payload.
func DecodeListRequest(rawPayload json.RawMessage) (ListRequest, []connection.ValidationIssue) {
	_, issues := rpc.ObjectFields(rawPayload)
	return ListRequest{}, issues
}

// DecodeCreateRequest validates an existing-directory adoption request.
func DecodeCreateRequest(rawPayload json.RawMessage) (CreateRequest, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) != 0 {
		return CreateRequest{}, issues
	}
	directory, fieldIssues := rpc.RequiredString(fields, "path", false)
	return CreateRequest{
		Path: directory,
	}, fieldIssues
}

// DecodeRenameRequest validates stable identity and non-blank title.
func DecodeRenameRequest(rawPayload json.RawMessage) (RenameRequest, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) != 0 {
		return RenameRequest{}, issues
	}
	identifier, identifierIssues := rpc.RequiredString(fields, "workspaceId", true)
	title, titleIssues := rpc.RequiredString(fields, "title", false)
	issues = append(issues, identifierIssues...)
	issues = append(issues, titleIssues...)
	if len(titleIssues) == 0 && strings.TrimSpace(title) == "" {
		issues = append(issues, connection.ValidationIssue{
			Code:    "custom",
			Path:    []string{},
			Message: "workspace.rename requires a non-blank title",
		})
	}
	return RenameRequest{
		WorkspaceID: ID(identifier),
		Title:       title,
	}, issues
}

// DecodeDeleteRequest validates one stable Workspace identity.
func DecodeDeleteRequest(rawPayload json.RawMessage) (DeleteRequest, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) != 0 {
		return DeleteRequest{}, issues
	}
	identifier, fieldIssues := rpc.RequiredString(fields, "workspaceId", true)
	return DeleteRequest{
		WorkspaceID: ID(identifier),
	}, fieldIssues
}

// DecodeInsertBeforeRequest validates the optional Workspace anchor.
func DecodeInsertBeforeRequest(
	rawPayload json.RawMessage,
) (InsertBeforeRequest, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) != 0 {
		return InsertBeforeRequest{}, issues
	}
	identifier, identifierIssues := rpc.RequiredString(fields, "workspaceId", true)
	before, beforeIssues := rpc.OptionalString(fields, "beforeWorkspaceId", true)
	issues = append(issues, identifierIssues...)
	issues = append(issues, beforeIssues...)
	var beforeIdentifier *ID
	if before != nil {
		converted := ID(*before)
		beforeIdentifier = &converted
	}
	return InsertBeforeRequest{
		WorkspaceID:       ID(identifier),
		BeforeWorkspaceID: beforeIdentifier,
	}, issues
}

// DecodeInsertSessionBeforeRequest validates Session source and anchor.
func DecodeInsertSessionBeforeRequest(
	rawPayload json.RawMessage,
) (InsertSessionBeforeRequest, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) != 0 {
		return InsertSessionBeforeRequest{}, issues
	}
	workspaceIdentifier, workspaceIssues := rpc.RequiredString(fields, "workspaceId", true)
	sessionIdentifier, sessionIssues := rpc.RequiredString(fields, "sessionId", true)
	before, beforeIssues := rpc.OptionalString(fields, "beforeSessionId", true)
	issues = append(issues, workspaceIssues...)
	issues = append(issues, sessionIssues...)
	issues = append(issues, beforeIssues...)
	var beforeIdentifier *sessionframes.SessionID
	if before != nil {
		converted := sessionframes.SessionID(*before)
		beforeIdentifier = &converted
	}
	return InsertSessionBeforeRequest{
		WorkspaceID:     ID(workspaceIdentifier),
		SessionID:       sessionframes.SessionID(sessionIdentifier),
		BeforeSessionID: beforeIdentifier,
	}, issues
}

// DecodeArchiveSessionRequest validates one known Session identity.
func DecodeArchiveSessionRequest(
	rawPayload json.RawMessage,
) (ArchiveSessionRequest, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) != 0 {
		return ArchiveSessionRequest{}, issues
	}
	identifier, fieldIssues := rpc.RequiredString(fields, "sessionId", true)
	return ArchiveSessionRequest{
		SessionID: sessionframes.SessionID(identifier),
	}, fieldIssues
}
