package connection

import (
	"bytes"
	"encoding/json"
)

const (
	// APIPath is the common prefix for DeepSeek Harness API carriers.
	APIPath = "/api"
	// RespondPath carries a ClientResponse back to the Host.
	RespondPath = APIPath + "/respond"
	// MuxEventsPath is the mux WebSocket downlink.
	MuxEventsPath = APIPath + "/events.mux"
	// HostEventsPath is the host WebSocket downlink.
	HostEventsPath = APIPath + "/events.host"
	// InvalidRequestRPCID keeps malformed request responses correlatable.
	InvalidRequestRPCID RPCID = "invalid-request"
)

const (
	ClientRequestType  = "client-request"
	ServerResponseType = "server-response"
	ServerRequestType  = "server-request"
	ClientResponseType = "client-response"
)

// RPCID correlates one request with its response. The initiator mints the ID;
// the responder only echoes it.
type RPCID string

// RPCErrorCode is the closed set of errors in the pinned TypeScript contract.
type RPCErrorCode string

const (
	ErrorBadRequest                  RPCErrorCode = "bad-request"
	ErrorCancelled                   RPCErrorCode = "cancelled"
	ErrorSessionNotFound             RPCErrorCode = "session-not-found"
	ErrorModelUnavailable            RPCErrorCode = "model-unavailable"
	ErrorSessionConflict             RPCErrorCode = "session-conflict"
	ErrorInvalidTimeZone             RPCErrorCode = "invalid-time-zone"
	ErrorWorkspaceAttachFailed       RPCErrorCode = "workspace-attach-failed"
	ErrorWorkspaceNotFound           RPCErrorCode = "workspace-not-found"
	ErrorWorkspaceInvalidPath        RPCErrorCode = "workspace-invalid-path"
	ErrorWorkspaceNameConflict       RPCErrorCode = "workspace-name-conflict"
	ErrorWorkspaceMoveInvalid        RPCErrorCode = "workspace-move-invalid"
	ErrorDirectoryUnreadable         RPCErrorCode = "directory-unreadable"
	ErrorDirectoryExists             RPCErrorCode = "directory-exists"
	ErrorDirectoryCreateFailed       RPCErrorCode = "directory-create-failed"
	ErrorDirectoryPickerUnavailable  RPCErrorCode = "directory-picker-unavailable"
	ErrorAgentPresetReadOnly         RPCErrorCode = "agent-preset-read-only"
	ErrorAgentPresetLocked           RPCErrorCode = "agent-preset-locked"
	ErrorAgentPresetConflict         RPCErrorCode = "agent-preset-conflict"
	ErrorAgentPresetNotFound         RPCErrorCode = "agent-preset-not-found"
	ErrorAgentPresetInvalid          RPCErrorCode = "agent-preset-invalid"
	ErrorAgentBusy                   RPCErrorCode = "agent-busy"
	ErrorAttachment                  RPCErrorCode = "attachment-error"
	ErrorQueueItemNotFound           RPCErrorCode = "queue-item-not-found"
	ErrorSteerUnavailable            RPCErrorCode = "steer-unavailable"
	ErrorCommand                     RPCErrorCode = "command-error"
	ErrorUnknownCommand              RPCErrorCode = "unknown-command"
	ErrorSettingsRejected            RPCErrorCode = "settings-rejected"
	ErrorSettingsNotExposed          RPCErrorCode = "settings-not-exposed"
	ErrorSettingsConflict            RPCErrorCode = "settings-conflict"
	ErrorCredentialRejected          RPCErrorCode = "credential-rejected"
	ErrorModelDiscoveryFailed        RPCErrorCode = "model-discovery-failed"
	ErrorTitleInvalid                RPCErrorCode = "title-invalid"
	ErrorForkUnavailable             RPCErrorCode = "fork-unavailable"
	ErrorSubagentParentUnavailable   RPCErrorCode = "subagent-parent-unavailable"
	ErrorSubagentNotFound            RPCErrorCode = "subagent-not-found"
	ErrorSubagentCatalogDiagnostic   RPCErrorCode = "subagent-catalog-diagnostic"
	ErrorSubagentNotResumable        RPCErrorCode = "subagent-not-resumable"
	ErrorSubagentUnauthorized        RPCErrorCode = "subagent-unauthorized"
	ErrorSubagentDeliveryUnavailable RPCErrorCode = "subagent-delivery-unavailable"
	ErrorBoundDefinitionExists       RPCErrorCode = "bound-definition-exists"
	ErrorBoundDefinitionNotFound     RPCErrorCode = "bound-definition-not-found"
	ErrorBoundDefinitionConflict     RPCErrorCode = "bound-definition-conflict"
	ErrorBoundDefinitionRejected     RPCErrorCode = "bound-definition-rejected"
	ErrorInternal                    RPCErrorCode = "internal"
)

// ValidationIssue is the stable JSON-safe subset of a Zod issue consumed by
// the TypeScript client. More issue fields can be added as method schemas need
// them without changing the bad-request envelope.
type ValidationIssue struct {
	Code     string   `json:"code"`
	Expected string   `json:"expected,omitempty"`
	Path     []string `json:"path"`
	Message  string   `json:"message"`
}

// BadRequestDetails is the details shape for the bad-request error code.
type BadRequestDetails struct {
	Issues []ValidationIssue `json:"issues"`
}

// RPCError is the wire business-error branch. Details remains raw only at the
// wire boundary; capability owners construct it from their named detail type.
type RPCError struct {
	Code    RPCErrorCode    `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details"`
}

// Valid reports whether the error uses a known code and the code-specific
// details shape required by the pinned wire contract.
func (resultError RPCError) Valid() bool {
	encoded, err := json.Marshal(resultError)
	if err != nil {
		return false
	}
	_, issues := decodeError(encoded)
	return len(issues) == 0
}

// RPCResult is the wire success/failure union. Value is absent only for a void
// success; Error is present only when OK is false.
type RPCResult struct {
	OK    bool            `json:"ok"`
	Value json.RawMessage `json:"value,omitempty"`
	Error *RPCError       `json:"error,omitempty"`
}

// ClientRequest is the full client-initiated unary request envelope.
type ClientRequest struct {
	Type    string          `json:"type"`
	RPCID   RPCID           `json:"rpcId"`
	Method  string          `json:"method"`
	Payload json.RawMessage `json:"payload"`
}

// ServerResponse is the full response envelope for a ClientRequest.
type ServerResponse struct {
	Type   string    `json:"type"`
	RPCID  RPCID     `json:"rpcId"`
	Result RPCResult `json:"result"`
}

// ServerRequest is one server-initiated push or answerable interaction.
type ServerRequest struct {
	Type    string          `json:"type"`
	RPCID   RPCID           `json:"rpcId"`
	Method  string          `json:"method"`
	Payload json.RawMessage `json:"payload"`
}

// ClientResponse answers one ServerRequest through POST /api/respond.
type ClientResponse struct {
	Type   string    `json:"type"`
	RPCID  RPCID     `json:"rpcId"`
	Result RPCResult `json:"result"`
}

// RPCReceipt is the carrier acknowledgement returned by POST /api/respond.
type RPCReceipt struct {
	Accepted bool             `json:"accepted"`
	Reason   RPCReceiptReason `json:"reason,omitempty"`
}

// RPCReceiptReason is the closed rejection reason set for /api/respond.
type RPCReceiptReason string

const (
	ReceiptNotPending  RPCReceiptReason = "not-pending"
	ReceiptBadResponse RPCReceiptReason = "bad-response"
)

// AcceptedReceipt acknowledges a well-formed answer to a pending request.
func AcceptedReceipt() RPCReceipt {
	return RPCReceipt{Accepted: true}
}

// RejectedReceipt rejects a response as either not-pending or bad-response.
func RejectedReceipt(reason RPCReceiptReason) RPCReceipt {
	return RPCReceipt{Accepted: false, Reason: reason}
}

// Success marshals a typed API value into the wire success branch.
func Success(value any) (RPCResult, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return RPCResult{}, err
	}
	return RPCResult{OK: true, Value: encoded}, nil
}

// Failure constructs the wire failure branch.
func Failure(resultError RPCError) RPCResult {
	return RPCResult{OK: false, Error: &resultError}
}

// BadRequest constructs the canonical payload or envelope validation error.
func BadRequest(message string, issues []ValidationIssue) RPCError {
	if issues == nil {
		issues = []ValidationIssue{}
	}
	details, err := json.Marshal(BadRequestDetails{Issues: issues})
	if err != nil {
		panic(err)
	}
	return RPCError{Code: ErrorBadRequest, Message: message, Details: details}
}

// DecodeClientRequest performs the first, envelope-level parse. Method payload
// validation remains the API Proxy's responsibility.
func DecodeClientRequest(body json.RawMessage) (ClientRequest, []ValidationIssue) {
	if !isJSONObject(body) {
		return ClientRequest{}, []ValidationIssue{objectIssue(nil)}
	}
	var fields struct {
		Type    json.RawMessage `json:"type"`
		RPCID   json.RawMessage `json:"rpcId"`
		Method  json.RawMessage `json:"method"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(body, &fields); err != nil {
		return ClientRequest{}, []ValidationIssue{objectIssue(nil)}
	}

	messageType, typeOK := decodeString(fields.Type)
	correlationID, idOK := decodeString(fields.RPCID)
	method, methodOK := decodeString(fields.Method)
	issues := make([]ValidationIssue, 0, 4)
	if !typeOK || messageType != ClientRequestType {
		issues = append(issues, literalIssue("type", ClientRequestType))
	}
	if !idOK {
		issues = append(issues, stringIssue("rpcId"))
	}
	if !methodOK {
		issues = append(issues, stringIssue("method"))
	}
	if fields.Payload == nil {
		issues = append(issues, requiredIssue("payload"))
	}
	if len(issues) != 0 {
		return ClientRequest{}, issues
	}
	return ClientRequest{
		Type: messageType, RPCID: RPCID(correlationID), Method: method,
		Payload: cloneRaw(fields.Payload),
	}, nil
}

// DecodeClientResponse validates the carrier envelope received by /api/respond.
func DecodeClientResponse(body json.RawMessage) (ClientResponse, []ValidationIssue) {
	if !isJSONObject(body) {
		return ClientResponse{}, []ValidationIssue{objectIssue(nil)}
	}
	var fields struct {
		Type   json.RawMessage `json:"type"`
		RPCID  json.RawMessage `json:"rpcId"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &fields); err != nil {
		return ClientResponse{}, []ValidationIssue{objectIssue(nil)}
	}

	messageType, typeOK := decodeString(fields.Type)
	correlationID, idOK := decodeString(fields.RPCID)
	issues := make([]ValidationIssue, 0, 3)
	if !typeOK || messageType != ClientResponseType {
		issues = append(issues, literalIssue("type", ClientResponseType))
	}
	if !idOK {
		issues = append(issues, stringIssue("rpcId"))
	}
	outcome, resultIssues := decodeResult(fields.Result)
	issues = append(issues, resultIssues...)
	if len(issues) != 0 {
		return ClientResponse{}, issues
	}
	return ClientResponse{Type: messageType, RPCID: RPCID(correlationID), Result: outcome}, nil
}

func decodeResult(raw json.RawMessage) (RPCResult, []ValidationIssue) {
	if !isJSONObject(raw) {
		return RPCResult{}, []ValidationIssue{objectIssue([]string{"result"})}
	}
	var fields struct {
		OK    json.RawMessage `json:"ok"`
		Value json.RawMessage `json:"value"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return RPCResult{}, []ValidationIssue{objectIssue([]string{"result"})}
	}
	var ok bool
	if len(fields.OK) == 0 || bytes.Equal(bytes.TrimSpace(fields.OK), []byte("null")) || json.Unmarshal(fields.OK, &ok) != nil {
		return RPCResult{}, []ValidationIssue{{Code: "invalid_type", Expected: "boolean", Path: []string{"result", "ok"}, Message: "Invalid input: expected boolean"}}
	}
	if ok {
		return RPCResult{OK: true, Value: cloneRaw(fields.Value)}, nil
	}
	parsedError, issues := decodeError(fields.Error)
	if len(issues) != 0 {
		return RPCResult{}, issues
	}
	return RPCResult{OK: false, Error: &parsedError}, nil
}

func decodeError(raw json.RawMessage) (RPCError, []ValidationIssue) {
	if !isJSONObject(raw) {
		return RPCError{}, []ValidationIssue{objectIssue([]string{"result", "error"})}
	}
	var fields struct {
		Code    json.RawMessage `json:"code"`
		Message json.RawMessage `json:"message"`
		Details json.RawMessage `json:"details"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return RPCError{}, []ValidationIssue{objectIssue([]string{"result", "error"})}
	}
	code, codeOK := decodeString(fields.Code)
	message, messageOK := decodeString(fields.Message)
	issues := make([]ValidationIssue, 0, 3)
	detailRules, codeExists := rpcErrorDetailRules[RPCErrorCode(code)]
	if !codeOK || !codeExists {
		issues = append(issues, ValidationIssue{Code: "invalid_value", Path: []string{"result", "error", "code"}, Message: "Invalid RPC error code"})
	}
	if !messageOK {
		issues = append(issues, ValidationIssue{Code: "invalid_type", Expected: "string", Path: []string{"result", "error", "message"}, Message: "Invalid input: expected string"})
	}
	if codeOK && codeExists && !detailRules.valid(fields.Details) {
		issues = append(issues, ValidationIssue{Code: "invalid_type", Expected: "object", Path: []string{"result", "error", "details"}, Message: "Invalid RPC error details"})
	}
	if len(issues) != 0 {
		return RPCError{}, issues
	}
	return RPCError{Code: RPCErrorCode(code), Message: message, Details: cloneRaw(fields.Details)}, nil
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '{'
}

func decodeString(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '"' {
		return "", false
	}
	var value string
	if json.Unmarshal(trimmed, &value) != nil {
		return "", false
	}
	return value, true
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func objectIssue(path []string) ValidationIssue {
	if path == nil {
		path = []string{}
	}
	return ValidationIssue{Code: "invalid_type", Expected: "object", Path: path, Message: "Invalid input: expected object"}
}

func literalIssue(field string, expected string) ValidationIssue {
	return ValidationIssue{Code: "invalid_value", Expected: expected, Path: []string{field}, Message: "Invalid literal value"}
}

func stringIssue(field string) ValidationIssue {
	return ValidationIssue{Code: "invalid_type", Expected: "string", Path: []string{field}, Message: "Invalid input: expected string"}
}

func requiredIssue(field string) ValidationIssue {
	return ValidationIssue{Code: "invalid_type", Path: []string{field}, Message: "Required field is missing"}
}
