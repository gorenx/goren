package apiproxy

import (
	"encoding/json"
	"strings"

	"github.com/gorenx/goren/connection"
)

// DecodeWorkspaceListRequest validates the empty-object baseline payload.
func DecodeWorkspaceListRequest(rawPayload json.RawMessage) (WorkspaceListRequest, []connection.ValidationIssue) {
	_, issues := decodeRequestObject(rawPayload)
	return WorkspaceListRequest{}, issues
}

// DecodeWorkspaceCreateRequest validates an existing-directory adoption request.
func DecodeWorkspaceCreateRequest(rawPayload json.RawMessage) (WorkspaceCreateRequest, []connection.ValidationIssue) {
	fields, issues := decodeRequestObject(rawPayload)
	if len(issues) != 0 {
		return WorkspaceCreateRequest{}, issues
	}
	directory, fieldIssues := requiredStringField(fields, "path", false)
	return WorkspaceCreateRequest{Path: directory}, fieldIssues
}

// DecodeWorkspaceRenameRequest validates stable identity and non-blank title.
func DecodeWorkspaceRenameRequest(rawPayload json.RawMessage) (WorkspaceRenameRequest, []connection.ValidationIssue) {
	fields, issues := decodeRequestObject(rawPayload)
	if len(issues) != 0 {
		return WorkspaceRenameRequest{}, issues
	}
	identifier, identifierIssues := requiredStringField(fields, "workspaceId", true)
	title, titleIssues := requiredStringField(fields, "title", false)
	issues = append(issues, identifierIssues...)
	issues = append(issues, titleIssues...)
	if len(titleIssues) == 0 && strings.TrimSpace(title) == "" {
		issues = append(issues, connection.ValidationIssue{
			Code: "custom", Path: []string{}, Message: "workspace.rename requires a non-blank title",
		})
	}
	return WorkspaceRenameRequest{WorkspaceID: WorkspaceID(identifier), Title: title}, issues
}

// DecodeWorkspaceDeleteRequest validates one stable Workspace identity.
func DecodeWorkspaceDeleteRequest(rawPayload json.RawMessage) (WorkspaceDeleteRequest, []connection.ValidationIssue) {
	fields, issues := decodeRequestObject(rawPayload)
	if len(issues) != 0 {
		return WorkspaceDeleteRequest{}, issues
	}
	identifier, fieldIssues := requiredStringField(fields, "workspaceId", true)
	return WorkspaceDeleteRequest{WorkspaceID: WorkspaceID(identifier)}, fieldIssues
}

// DecodeWorkspaceInsertBeforeRequest validates the optional Workspace anchor.
func DecodeWorkspaceInsertBeforeRequest(
	rawPayload json.RawMessage,
) (WorkspaceInsertBeforeRequest, []connection.ValidationIssue) {
	fields, issues := decodeRequestObject(rawPayload)
	if len(issues) != 0 {
		return WorkspaceInsertBeforeRequest{}, issues
	}
	identifier, identifierIssues := requiredStringField(fields, "workspaceId", true)
	before, beforeIssues := optionalStringField(fields, "beforeWorkspaceId", true)
	issues = append(issues, identifierIssues...)
	issues = append(issues, beforeIssues...)
	var beforeIdentifier *WorkspaceID
	if before != nil {
		converted := WorkspaceID(*before)
		beforeIdentifier = &converted
	}
	return WorkspaceInsertBeforeRequest{
		WorkspaceID: WorkspaceID(identifier), BeforeWorkspaceID: beforeIdentifier,
	}, issues
}

// DecodeWorkspaceInsertSessionBeforeRequest validates Session source and anchor.
func DecodeWorkspaceInsertSessionBeforeRequest(
	rawPayload json.RawMessage,
) (WorkspaceInsertSessionBeforeRequest, []connection.ValidationIssue) {
	fields, issues := decodeRequestObject(rawPayload)
	if len(issues) != 0 {
		return WorkspaceInsertSessionBeforeRequest{}, issues
	}
	workspaceIdentifier, workspaceIssues := requiredStringField(fields, "workspaceId", true)
	sessionIdentifier, sessionIssues := requiredStringField(fields, "sessionId", true)
	before, beforeIssues := optionalStringField(fields, "beforeSessionId", true)
	issues = append(issues, workspaceIssues...)
	issues = append(issues, sessionIssues...)
	issues = append(issues, beforeIssues...)
	var beforeIdentifier *SessionID
	if before != nil {
		converted := SessionID(*before)
		beforeIdentifier = &converted
	}
	return WorkspaceInsertSessionBeforeRequest{
		WorkspaceID: WorkspaceID(workspaceIdentifier), SessionID: SessionID(sessionIdentifier),
		BeforeSessionID: beforeIdentifier,
	}, issues
}

// DecodeWorkspaceArchiveSessionRequest validates one known Session identity.
func DecodeWorkspaceArchiveSessionRequest(
	rawPayload json.RawMessage,
) (WorkspaceArchiveSessionRequest, []connection.ValidationIssue) {
	fields, issues := decodeRequestObject(rawPayload)
	if len(issues) != 0 {
		return WorkspaceArchiveSessionRequest{}, issues
	}
	identifier, fieldIssues := requiredStringField(fields, "sessionId", true)
	return WorkspaceArchiveSessionRequest{SessionID: SessionID(identifier)}, fieldIssues
}
