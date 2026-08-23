package continuation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/tools"
)

func (owner *Manager) snapshotStart(
	startSpec subagent.ContinuableStartSpec,
) (subagent.ContinuableRequest, subagent.ContinuableDescriptor, error) {
	requestSnapshot, snapshotErr := cloneRequest(startSpec.Request)
	if snapshotErr != nil {
		return subagent.ContinuableRequest{}, subagent.ContinuableDescriptor{}, snapshotErr
	}
	if requestSnapshot.Parent == nil ||
		!owner.dependencies.Agents.Contains(requestSnapshot.Parent) {
		return subagent.ContinuableRequest{}, subagent.ContinuableDescriptor{},
			unauthorized("continuable Start requires the exact live parent Agent")
	}
	childDepth, depthErr := resolveDepth(
		requestSnapshot.Parent,
		requestSnapshot.MaxDepth,
	)
	if depthErr != nil {
		return subagent.ContinuableRequest{}, subagent.ContinuableDescriptor{}, depthErr
	}
	requestSnapshot.AgentOptions = resolveOptions(
		requestSnapshot.Parent,
		requestSnapshot.AgentOptions,
		childDepth,
	)
	resolvedOptions := requestSnapshot.AgentOptions
	identityData, snapshotErr := subagent.SnapshotDescriptor(
		subagent.ContinuableDescriptor{
			Provider:      startSpec.Provider,
			Label:         startSpec.Label,
			AgentProvider: stringPointer(resolvedOptions.Provider),
			AgentModel:    stringPointer(resolvedOptions.Model),
			Persona:       cloneString(requestSnapshot.Persona),
			ToolFilter:    requestSnapshot.ToolFilter,
		},
	)
	if snapshotErr != nil {
		return subagent.ContinuableRequest{}, subagent.ContinuableDescriptor{}, snapshotErr
	}
	identity, matches := identityData.DescriptorValue().(subagent.ContinuableDescriptor)
	if !matches {
		return subagent.ContinuableRequest{}, subagent.ContinuableDescriptor{},
			errors.New("subagent: continuable descriptor snapshot changed variant")
	}
	return requestSnapshot, identity, nil
}

func (owner *Manager) prepareProvider(
	providerName string,
) (subagent.ContinuableProvider, error) {
	candidate, found := owner.dependencies.Providers.Get(providerName)
	if !found {
		return nil, &subagent.Error{
			Code:    subagent.ErrorNoProvider,
			Message: fmt.Sprintf("no subagent Provider registered for %q", providerName),
		}
	}
	continuationProvider, supported := candidate.(subagent.ContinuableProvider)
	if !supported {
		return nil, &subagent.Error{
			Code: subagent.ErrorUnsupportedCapability,
			Message: fmt.Sprintf(
				"subagent Provider %q does not support continuable children",
				providerName,
			),
		}
	}
	return continuationProvider, nil
}

func descriptorSeed(
	childID session.SessionID,
	providerSeed []session.Event,
	identity subagent.ContinuableDescriptor,
) ([]session.Event, error) {
	staged, stageErr := session.New(
		childID,
		session.CreateOptions{
			Seed: providerSeed,
		},
	)
	if stageErr != nil {
		return nil, stageErr
	}
	identityData, snapshotErr := subagent.SnapshotDescriptor(identity)
	if snapshotErr != nil {
		return nil, snapshotErr
	}
	if _, appendErr := session.AppendSerialized(
		staged,
		subagent.DescriptorEvent,
		identityData,
	); appendErr != nil {
		return nil, appendErr
	}
	return staged.Events(), nil
}

func cloneRequest(
	source subagent.ContinuableRequest,
) (subagent.ContinuableRequest, error) {
	promptSnapshot, cloneErr := llm.CloneContentBlocks(source.Prompt)
	if cloneErr != nil {
		return subagent.ContinuableRequest{}, cloneErr
	}
	filterSnapshot, snapshotErr := cloneRestriction(source.ToolFilter)
	if snapshotErr != nil {
		return subagent.ContinuableRequest{}, snapshotErr
	}
	return subagent.ContinuableRequest{
		Prompt:       promptSnapshot,
		Parent:       source.Parent,
		AgentOptions: cloneOptions(source.AgentOptions),
		MaxDepth:     cloneInt64(source.MaxDepth),
		ToolFilter:   filterSnapshot,
		Persona:      cloneString(source.Persona),
	}, nil
}

func resolveDepth(parentAgent agent.Agent, maxDepth *int64) (int64, error) {
	if maxDepth != nil && (*maxDepth < 0 || *maxDepth > maxSafeInteger) {
		return 0, errors.New("subagent: maxDepth must be a non-negative safe integer")
	}
	parentDepth := int64(0)
	parentOptions := parentAgent.OptionsValue()
	if parentOptions.SubagentDepth != nil {
		parentDepth = *parentOptions.SubagentDepth
	}
	header := parentAgent.SessionValue().Header()
	if header.DelegationDepth != nil && *header.DelegationDepth > parentDepth {
		parentDepth = *header.DelegationDepth
	}
	if parentDepth >= maxSafeInteger {
		return 0, errors.New("subagent: child depth exceeds the safe integer range")
	}
	childDepth := parentDepth + 1
	if maxDepth != nil && childDepth > *maxDepth {
		return 0, fmt.Errorf(
			"subagent: child depth %d exceeds maxDepth %d",
			childDepth,
			*maxDepth,
		)
	}
	return childDepth, nil
}

func resolveOptions(
	parentAgent agent.Agent,
	requested *agent.Options,
	childDepth int64,
) *agent.Options {
	resolved := parentAgent.OptionsValue()
	if requested != nil {
		if requested.Provider != "" {
			resolved.Provider = requested.Provider
		}
		if requested.Model != "" {
			resolved.Model = requested.Model
		}
		if requested.MaxTokens != nil {
			maxTokensValue := *requested.MaxTokens
			resolved.MaxTokens = &maxTokensValue
		}
	}
	resolved.SubagentDepth = int64Pointer(childDepth)
	return &resolved
}

func cloneOptions(source *agent.Options) *agent.Options {
	if source == nil {
		return nil
	}
	detached := *source
	if source.MaxTokens != nil {
		maxTokensValue := *source.MaxTokens
		detached.MaxTokens = &maxTokensValue
	}
	detached.SubagentDepth = cloneInt64(source.SubagentDepth)
	return &detached
}

func cloneRestriction(
	filterValue *tools.ToolRestriction,
) (*tools.ToolRestriction, error) {
	if filterValue == nil {
		return nil, nil
	}
	if filterValue.Allow == nil && filterValue.Deny == nil {
		return nil, errors.New(
			"subagent: toolFilter must declare allow and/or deny",
		)
	}
	return &tools.ToolRestriction{
		Allow: cloneStrings(filterValue.Allow),
		Deny:  cloneStrings(filterValue.Deny),
	}, nil
}

func cloneStrings(source []string) []string {
	if source == nil {
		return nil
	}
	detached := make([]string, len(source))
	copy(detached, source)
	return detached
}

func checkContext(requestContext context.Context, operation string) error {
	if requestContext == nil {
		return errors.New("subagent: " + operation + " context is nil")
	}
	return requestContext.Err()
}

func unauthorized(message string) error {
	return &subagent.Error{
		Code:    subagent.ErrorUnauthorized,
		Message: message,
	}
}

func duplicateChild(childID session.SessionID) error {
	return &subagent.Error{
		Code:    subagent.ErrorDuplicateChild,
		Message: fmt.Sprintf("subagent %q already exists", childID),
	}
}

func newSessionID() (session.SessionID, error) {
	runID, generateErr := newRunID()
	return session.SessionID(runID), generateErr
}

func newRunID() (subagent.RunID, error) {
	var randomBytes [16]byte
	if _, readErr := rand.Read(randomBytes[:]); readErr != nil {
		return "", fmt.Errorf("subagent: generate identity: %w", readErr)
	}
	randomBytes[6] = randomBytes[6]&0x0f | 0x40
	randomBytes[8] = randomBytes[8]&0x3f | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], randomBytes[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], randomBytes[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], randomBytes[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], randomBytes[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], randomBytes[10:16])
	return subagent.RunID(encoded), nil
}

func cloneInt64(source *int64) *int64 {
	if source == nil {
		return nil
	}
	snapshot := *source
	return &snapshot
}

func cloneString(source *string) *string {
	if source == nil {
		return nil
	}
	snapshot := *source
	return &snapshot
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func sessionIDPointer(value session.SessionID) *session.SessionID {
	return &value
}

func int64Pointer(value int64) *int64 {
	return &value
}
