package apiproxy

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gorenx/goren/connection"
)

const (
	CredentialsDescribeMethod = "credentials.describe"
	CredentialsSetMethod      = "credentials.set"
	CredentialsUnsetMethod    = "credentials.unset"
)

// CredentialsDescribeRequest names the references a client is allowed to inspect.
type CredentialsDescribeRequest struct {
	Refs []string `json:"refs"`
}

// CredentialView is value-free credential state safe to return to a browser.
type CredentialView struct {
	Configured bool   `json:"configured"`
	Source     string `json:"source,omitempty"`
	Writable   bool   `json:"writable"`
}

// CredentialsDescribeValue contains one view per requested reference.
type CredentialsDescribeValue struct {
	Credentials map[string]CredentialView `json:"credentials"`
}

// CredentialsSetRequest is the only Host request that carries a credential value.
type CredentialsSetRequest struct {
	Ref   string `json:"ref"`
	Value string `json:"value"`
}

// CredentialsUnsetRequest removes one managed credential value.
type CredentialsUnsetRequest struct {
	Ref string `json:"ref"`
}

// CredentialsWriteValue is the canonical empty success result.
type CredentialsWriteValue struct{}

// CredentialsAPI owns the browser-safe Credentials methods.
type CredentialsAPI interface {
	Describe(context.Context, Request[CredentialsDescribeRequest]) (Outcome[CredentialsDescribeValue], error)
	Set(context.Context, Request[CredentialsSetRequest]) (Outcome[CredentialsWriteValue], error)
	Unset(context.Context, Request[CredentialsUnsetRequest]) (Outcome[CredentialsWriteValue], error)
}

// RegisterCredentialsAPI installs the fixed DeepSeek Harness credential surface.
func RegisterCredentialsAPI(methods *Catalog, gateway CredentialsAPI) error {
	if err := RegisterUnary(methods, CredentialsDescribeMethod, DecodeCredentialsDescribeRequest, gateway.Describe); err != nil {
		return err
	}
	if err := RegisterUnary(methods, CredentialsSetMethod, DecodeCredentialsSetRequest, gateway.Set); err != nil {
		return err
	}
	return RegisterUnary(methods, CredentialsUnsetMethod, DecodeCredentialsUnsetRequest, gateway.Unset)
}

// DecodeCredentialsDescribeRequest validates the source schema's bounded reference list.
func DecodeCredentialsDescribeRequest(rawPayload json.RawMessage) (CredentialsDescribeRequest, []connection.ValidationIssue) {
	fields, issues := decodeRequestObject(rawPayload)
	if len(issues) != 0 {
		return CredentialsDescribeRequest{}, issues
	}
	rawRefs, found := fields["refs"]
	if !found {
		return CredentialsDescribeRequest{}, []connection.ValidationIssue{invalidTypeIssue([]string{"refs"}, "array")}
	}
	var refs []json.RawMessage
	if err := json.Unmarshal(rawRefs, &refs); err != nil {
		return CredentialsDescribeRequest{}, []connection.ValidationIssue{invalidTypeIssue([]string{"refs"}, "array")}
	}
	if len(refs) > 64 {
		issues = append(issues, connection.ValidationIssue{
			Code: "too_big", Path: []string{"refs"}, Message: "Too big: expected array to have <=64 items",
		})
	}
	decoded := make([]string, 0, len(refs))
	for index, rawRef := range refs {
		var ref string
		if err := json.Unmarshal(rawRef, &ref); err != nil {
			issues = append(issues, invalidTypeIssue([]string{"refs", jsonIndex(index)}, "string"))
			continue
		}
		if !validCredentialReference(ref) {
			issues = append(issues, invalidCredentialReferenceIssue([]string{"refs", jsonIndex(index)}))
			continue
		}
		decoded = append(decoded, ref)
	}
	return CredentialsDescribeRequest{Refs: decoded}, issues
}

// DecodeCredentialsSetRequest validates reference and one non-empty value.
func DecodeCredentialsSetRequest(rawPayload json.RawMessage) (CredentialsSetRequest, []connection.ValidationIssue) {
	fields, issues := decodeRequestObject(rawPayload)
	if len(issues) != 0 {
		return CredentialsSetRequest{}, issues
	}
	ref, refIssues := requiredStringField(fields, "ref", true)
	value, valueIssues := requiredStringField(fields, "value", true)
	issues = append(issues, refIssues...)
	issues = append(issues, valueIssues...)
	if len(refIssues) == 0 && !validCredentialReference(ref) {
		issues = append(issues, invalidCredentialReferenceIssue([]string{"ref"}))
	}
	return CredentialsSetRequest{Ref: ref, Value: value}, issues
}

// DecodeCredentialsUnsetRequest validates one reference.
func DecodeCredentialsUnsetRequest(rawPayload json.RawMessage) (CredentialsUnsetRequest, []connection.ValidationIssue) {
	fields, issues := decodeRequestObject(rawPayload)
	if len(issues) != 0 {
		return CredentialsUnsetRequest{}, issues
	}
	ref, refIssues := requiredStringField(fields, "ref", true)
	if len(refIssues) == 0 && !validCredentialReference(ref) {
		refIssues = append(refIssues, invalidCredentialReferenceIssue([]string{"ref"}))
	}
	return CredentialsUnsetRequest{Ref: ref}, refIssues
}

func validCredentialReference(value string) bool {
	if value == "" {
		return false
	}
	for index, character := range value {
		if character == '_' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' {
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
		Code: "invalid_format", Path: path,
		Message: "Invalid string: must match pattern /^[A-Za-z_][A-Za-z0-9_]*$/",
	}
}

func jsonIndex(index int) string {
	return fmt.Sprintf("%d", index)
}
