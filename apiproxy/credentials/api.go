package credentialsapi

import (
	"context"
	"encoding/json"
	rpc "github.com/gorenx/goren/apiproxy/rpc"
	"strconv"

	"github.com/gorenx/goren/connection"
)

const (
	DescribeMethod = "credentials.describe"
	SetMethod      = "credentials.set"
	UnsetMethod    = "credentials.unset"
)

// DescribeRequest names the references a client is allowed to inspect.
type DescribeRequest struct {
	Refs []string `json:"refs"`
}

// View is value-free credential state safe to return to a browser.
type View struct {
	Configured bool   `json:"configured"`
	Source     string `json:"source,omitempty"`
	Writable   bool   `json:"writable"`
}

// DescribeValue contains one view per requested reference.
type DescribeValue struct {
	Credentials map[string]View `json:"credentials"`
}

// SetRequest is the only Host request that carries a credential value.
type SetRequest struct {
	Ref   string `json:"ref"`
	Value string `json:"value"`
}

// UnsetRequest removes one managed credential value.
type UnsetRequest struct {
	Ref string `json:"ref"`
}

// WriteValue is the canonical empty success result.
type WriteValue struct{}

// API owns the browser-safe Credentials methods.
type API interface {
	Describe(context.Context, rpc.Request[DescribeRequest]) (rpc.Outcome[DescribeValue], error)
	Set(context.Context, rpc.Request[SetRequest]) (rpc.Outcome[WriteValue], error)
	Unset(context.Context, rpc.Request[UnsetRequest]) (rpc.Outcome[WriteValue], error)
}

// Register installs the fixed DeepSeek Harness credential surface.
func Register(methods *rpc.Catalog, endpoint API) error {
	if err := rpc.RegisterUnary(methods, DescribeMethod, DecodeDescribeRequest, endpoint.Describe); err != nil {
		return err
	}
	if err := rpc.RegisterUnary(methods, SetMethod, DecodeSetRequest, endpoint.Set); err != nil {
		return err
	}
	return rpc.RegisterUnary(methods, UnsetMethod, DecodeUnsetRequest, endpoint.Unset)
}

// DecodeDescribeRequest validates the source schema's bounded reference list.
func DecodeDescribeRequest(rawPayload json.RawMessage) (DescribeRequest, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) != 0 {
		return DescribeRequest{}, issues
	}
	rawRefs, found := fields["refs"]
	if !found {
		return DescribeRequest{}, []connection.ValidationIssue{rpc.InvalidType([]string{"refs"}, "array")}
	}
	var refs []json.RawMessage
	if err := json.Unmarshal(rawRefs, &refs); err != nil {
		return DescribeRequest{}, []connection.ValidationIssue{rpc.InvalidType([]string{"refs"}, "array")}
	}
	if len(refs) > 64 {
		issues = append(issues, connection.ValidationIssue{
			Code:    "too_big",
			Path:    []string{"refs"},
			Message: "Too big: expected array to have <=64 items",
		})
	}
	decoded := make([]string, 0, len(refs))
	for index, rawRef := range refs {
		var ref string
		if err := json.Unmarshal(rawRef, &ref); err != nil {
			issues = append(issues, rpc.InvalidType([]string{"refs", strconv.Itoa(index)}, "string"))
			continue
		}
		if !validCredentialReference(ref) {
			issues = append(issues, invalidCredentialReferenceIssue([]string{"refs", strconv.Itoa(index)}))
			continue
		}
		decoded = append(decoded, ref)
	}
	return DescribeRequest{
		Refs: decoded,
	}, issues
}

// DecodeSetRequest validates reference and one non-empty value.
func DecodeSetRequest(rawPayload json.RawMessage) (SetRequest, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) != 0 {
		return SetRequest{}, issues
	}
	ref, refIssues := rpc.RequiredString(fields, "ref", true)
	value, valueIssues := rpc.RequiredString(fields, "value", true)
	issues = append(issues, refIssues...)
	issues = append(issues, valueIssues...)
	if len(refIssues) == 0 && !validCredentialReference(ref) {
		issues = append(issues, invalidCredentialReferenceIssue([]string{"ref"}))
	}
	return SetRequest{
		Ref:   ref,
		Value: value,
	}, issues
}

// DecodeUnsetRequest validates one reference.
func DecodeUnsetRequest(rawPayload json.RawMessage) (UnsetRequest, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) != 0 {
		return UnsetRequest{}, issues
	}
	ref, refIssues := rpc.RequiredString(fields, "ref", true)
	if len(refIssues) == 0 && !validCredentialReference(ref) {
		refIssues = append(refIssues, invalidCredentialReferenceIssue([]string{"ref"}))
	}
	return UnsetRequest{
		Ref: ref,
	}, refIssues
}

func validCredentialReference(value string) bool {
	if value == "" {
		return false
	}
	for index, character := range value {
		if character == '_' ||
			character >= 'A' && character <= 'Z' ||
			character >= 'a' && character <= 'z' {
			continue
		}
		if index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}

func invalidCredentialReferenceIssue(path []string) connection.ValidationIssue {
	return connection.ValidationIssue{
		Code:    "invalid_format",
		Path:    path,
		Message: "Invalid string: must match pattern /^[A-Za-z_][A-Za-z0-9_]*$/",
	}
}
