package session

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/gorenx/goren/llm"
)

// LatestRequestHeader folds detached Session facts into the latest canonical
// request header. A nil result means no request/header fact has been committed.
func LatestRequestHeader(entries []Event) (*EpochHeader, error) {
	var latest *EpochHeader
	for _, entry := range entries {
		if entry.Type != RequestHeaderEventName {
			continue
		}
		var payload RequestHeaderSnapshot
		if err := decodeSessionPayload(entry.Data, &payload); err != nil {
			return nil, fmt.Errorf(
				"session: invalid request/header at seq %d: %w",
				entry.Seq,
				err,
			)
		}
		canonical := CanonicalEpochHeader(payload.Header)
		latest = &canonical
	}
	if latest == nil {
		return nil, nil
	}
	detached := CanonicalEpochHeader(*latest)
	return &detached, nil
}

// LatestRequestContext folds detached Session facts into the latest resolved
// route capacity. A nil result means no request/context fact has been committed.
func LatestRequestContext(entries []Event) (*RequestRouteContext, error) {
	var latest *RequestRouteContext
	for _, entry := range entries {
		if entry.Type != RequestContextEventName {
			continue
		}
		var payload RequestRouteContext
		if err := decodeSessionPayload(entry.Data, &payload); err != nil {
			return nil, fmt.Errorf(
				"session: invalid request/context at seq %d: %w",
				entry.Seq,
				err,
			)
		}
		if payload.Provider == "" || payload.Model == "" ||
			(payload.ContextWindow != nil && *payload.ContextWindow <= 0) {
			return nil, fmt.Errorf(
				"session: invalid request/context at seq %d",
				entry.Seq,
			)
		}
		contextSnapshot := payload
		contextSnapshot.ContextWindow = cloneInt(payload.ContextWindow)
		latest = &contextSnapshot
	}
	if latest == nil {
		return nil, nil
	}
	result := *latest
	result.ContextWindow = cloneInt(latest.ContextWindow)
	return &result, nil
}

// CanonicalEpochHeader detaches a request header and removes empty optional fields.
func CanonicalEpochHeader(inputSnapshot EpochHeader) EpochHeader {
	canonical := EpochHeader{
		Config: llm.CloneCallConfig(inputSnapshot.Config),
	}
	if inputSnapshot.AdapterDefaults != nil &&
		(inputSnapshot.AdapterDefaults.ReasoningEffort || inputSnapshot.AdapterDefaults.MaxTokens) {
		defaultsSnapshot := *inputSnapshot.AdapterDefaults
		canonical.AdapterDefaults = &defaultsSnapshot
	}
	if inputSnapshot.System != nil && *inputSnapshot.System != "" {
		promptSnapshot := *inputSnapshot.System
		canonical.System = &promptSnapshot
	}
	if len(inputSnapshot.Tools) != 0 {
		canonical.Tools = cloneToolSchemas(inputSnapshot.Tools)
	}
	return canonical
}

// EpochHeaderEqual compares canonical request headers, including ordered schemas.
func EpochHeaderEqual(left EpochHeader, right EpochHeader) bool {
	leftCanonical := CanonicalEpochHeader(left)
	rightCanonical := CanonicalEpochHeader(right)
	if !llm.CallConfigEqual(leftCanonical.Config, rightCanonical.Config) ||
		!sameAdapterDefaults(leftCanonical.AdapterDefaults, rightCanonical.AdapterDefaults) ||
		!sameOptionalString(leftCanonical.System, rightCanonical.System) ||
		len(leftCanonical.Tools) != len(rightCanonical.Tools) {
		return false
	}
	for index := range leftCanonical.Tools {
		leftSchema := leftCanonical.Tools[index]
		rightSchema := rightCanonical.Tools[index]
		if leftSchema.Name != rightSchema.Name ||
			leftSchema.Description != rightSchema.Description ||
			!bytes.Equal(leftSchema.Parameters, rightSchema.Parameters) {
			return false
		}
	}
	return true
}

func cloneToolSchemas(entries []llm.ToolSchema) []llm.ToolSchema {
	detached := make([]llm.ToolSchema, len(entries))
	for index, entry := range entries {
		detached[index] = entry
		detached[index].Parameters = append(json.RawMessage(nil), entry.Parameters...)
	}
	return detached
}

func sameAdapterDefaults(left *llm.CallConfigAdapterDefaults, right *llm.CallConfigAdapterDefaults) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameOptionalString(left *string, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneInt(source *int) *int {
	if source == nil {
		return nil
	}
	copyValue := *source
	return &copyValue
}
