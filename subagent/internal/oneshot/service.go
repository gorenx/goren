package oneshot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/tools"
)

const maxSafeInteger int64 = 1<<53 - 1

// Providers resolves an exact currently registered Provider.
type Providers interface {
	Get(string) (subagent.Provider, bool)
}

// Lifecycle publishes paired one-shot lifecycle facts.
type Lifecycle interface {
	Started(agent.Agent, subagent.Started)
	Ended(agent.Agent, subagent.Ended)
}

// Service owns one-shot admission and lifecycle observation.
type Service struct {
	providers Providers
	lifecycle Lifecycle
}

// New constructs a one-shot application service.
func New(providerSource Providers, lifecycleTarget Lifecycle) *Service {
	return &Service{
		providers: providerSource,
		lifecycle: lifecycleTarget,
	}
}

// Start validates and dispatches one one-shot child.
func (serviceOwner *Service) Start(
	requestContext context.Context,
	selectedName string,
	startInput subagent.StartRequest,
) (subagent.Run, error) {
	if requestContext == nil {
		return nil, errors.New("subagent: one-shot Start context is nil")
	}
	if requestErr := requestContext.Err(); requestErr != nil {
		return nil, requestErr
	}
	candidate, found := serviceOwner.providers.Get(selectedName)
	if !found {
		return nil, &subagent.Error{
			Code: subagent.ErrorNoProvider,
			Message: fmt.Sprintf(
				"no subagent provider registered for %q",
				selectedName,
			),
		}
	}
	resolved, resolveErr := resolveRequest(
		selectedName,
		candidate.Capabilities(),
		startInput,
	)
	if resolveErr != nil {
		return nil, resolveErr
	}
	runIdentity, identityErr := newRunID()
	if identityErr != nil {
		return nil, identityErr
	}
	runHandle, startErr := candidate.Start(requestContext, resolved)
	if startErr != nil {
		return nil, startErr
	}
	if validationErr := validateRun(runHandle); validationErr != nil {
		if runHandle == nil || nilInterface(runHandle) {
			return nil, validationErr
		}
		disposeErr := runHandle.Dispose(context.Background())
		return nil, errors.Join(validationErr, disposeErr)
	}
	serviceOwner.observe(
		runIdentity,
		selectedName,
		resolved.Parent,
		runHandle,
	)
	return runHandle, nil
}

func resolveRequest(
	selectedName string,
	capabilitySet subagent.Capabilities,
	startInput subagent.StartRequest,
) (subagent.ResolvedStartRequest, error) {
	if startInput.Parent == nil || nilInterface(startInput.Parent) {
		return subagent.ResolvedStartRequest{}, errors.New(
			"subagent: one-shot Start requires a parent Agent",
		)
	}
	if depthErr := validateMaxDepth(startInput.MaxDepth); depthErr != nil {
		return subagent.ResolvedStartRequest{}, depthErr
	}
	if capabilityErr := assertCapabilities(
		selectedName,
		capabilitySet,
		startInput,
	); capabilityErr != nil {
		return subagent.ResolvedStartRequest{}, capabilityErr
	}
	promptSnapshot, cloneErr := llm.CloneContentBlocks(startInput.Prompt)
	if cloneErr != nil {
		return subagent.ResolvedStartRequest{}, fmt.Errorf(
			"subagent: snapshot one-shot prompt: %w",
			cloneErr,
		)
	}
	agentSnapshot := cloneAgentOptions(startInput.AgentOptions)
	filterSnapshot, cloneErr := cloneToolRestriction(startInput.ToolFilter)
	if cloneErr != nil {
		return subagent.ResolvedStartRequest{}, cloneErr
	}
	var schemaSnapshot json.RawMessage
	if len(startInput.OutputSchema) != 0 {
		schemaSnapshot, cloneErr = tools.SnapshotObjectSchema(
			startInput.OutputSchema,
		)
		if cloneErr != nil {
			return subagent.ResolvedStartRequest{}, cloneErr
		}
	}
	identitySnapshot, cloneErr := subagent.SnapshotDescriptor(
		subagent.OneShotDescriptor{
			Provider: selectedName,
			Label:    cloneString(startInput.Label),
		},
	)
	if cloneErr != nil {
		return subagent.ResolvedStartRequest{}, cloneErr
	}
	oneShotIdentity, matches := identitySnapshot.DescriptorValue().(subagent.OneShotDescriptor)
	if !matches {
		return subagent.ResolvedStartRequest{}, errors.New(
			"subagent: one-shot descriptor snapshot changed variant",
		)
	}
	return subagent.ResolvedStartRequest{
		StartRequest: subagent.StartRequest{
			Label:        cloneString(startInput.Label),
			Prompt:       promptSnapshot,
			Parent:       startInput.Parent,
			AgentOptions: agentSnapshot,
			OutputSchema: schemaSnapshot,
			MaxDepth:     cloneInt64(startInput.MaxDepth),
			ToolFilter:   filterSnapshot,
			Persona:      cloneString(startInput.Persona),
		},
		Descriptor: oneShotIdentity,
	}, nil
}

func assertCapabilities(
	selectedName string,
	capabilitySet subagent.Capabilities,
	startInput subagent.StartRequest,
) error {
	requirements := []struct {
		needed    bool
		supported bool
		name      string
	}{
		{
			needed:    len(startInput.OutputSchema) != 0,
			supported: capabilitySet.OutputSchema,
			name:      "outputSchema",
		},
		{
			needed:    startInput.MaxDepth != nil,
			supported: capabilitySet.DepthLimit,
			name:      "depthLimit",
		},
		{
			needed:    startInput.ToolFilter != nil,
			supported: capabilitySet.ToolFilter,
			name:      "toolFilter",
		},
		{
			needed:    startInput.Persona != nil,
			supported: capabilitySet.Persona,
			name:      "persona",
		},
	}
	for _, requirement := range requirements {
		if requirement.needed && !requirement.supported {
			return &subagent.Error{
				Code: subagent.ErrorUnsupportedCapability,
				Message: fmt.Sprintf(
					"subagent provider %q does not support the %q capability",
					selectedName,
					requirement.name,
				),
			}
		}
	}
	return nil
}

func validateRun(runHandle subagent.Run) error {
	if runHandle == nil || nilInterface(runHandle) {
		return errors.New("subagent: Provider returned a nil Run")
	}
	childID := runHandle.ID()
	if childID == "" {
		return errors.New("subagent: Provider returned a Run with an empty id")
	}
	localChild, local := runHandle.LocalAgent()
	if !local {
		return nil
	}
	if localChild == nil || nilInterface(localChild) || localChild.ID() != childID {
		return errors.New(
			"subagent: Provider returned an inconsistent local Run identity",
		)
	}
	return nil
}

func (serviceOwner *Service) observe(
	runIdentity subagent.RunID,
	selectedName string,
	parentAgent agent.Agent,
	runHandle subagent.Run,
) {
	childID := runHandle.ID()
	_, local := runHandle.LocalAgent()
	startGate := make(chan struct{})
	go func() {
		terminal, resultErr := runHandle.AwaitResult(context.Background())
		<-startGate
		endFact := subagent.Ended{
			RunID:    runIdentity,
			Provider: selectedName,
			ID:       childID,
			Local:    local,
		}
		if resultErr != nil {
			endFact.StopReason = subagent.StopError
		} else {
			endFact.StopReason = terminal.StopReason
			outputSnapshot, cloneErr := llm.CloneContentBlocks(terminal.Output)
			if cloneErr != nil {
				endFact.StopReason = subagent.StopError
			} else {
				endFact.LastAssistantMessage = outputSnapshot
			}
		}
		if serviceOwner.lifecycle != nil {
			serviceOwner.lifecycle.Ended(parentAgent, endFact)
		}
	}()
	if serviceOwner.lifecycle != nil {
		serviceOwner.lifecycle.Started(
			parentAgent,
			subagent.Started{
				RunID:    runIdentity,
				Provider: selectedName,
				ID:       childID,
				Local:    local,
			},
		)
	}
	close(startGate)
}

func newRunID() (subagent.RunID, error) {
	var randomBytes [16]byte
	if _, readErr := rand.Read(randomBytes[:]); readErr != nil {
		return "", fmt.Errorf("subagent: generate RunID: %w", readErr)
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

func validateMaxDepth(maxDepth *int64) error {
	if maxDepth != nil && (*maxDepth < 0 || *maxDepth > maxSafeInteger) {
		return errors.New(
			"subagent: maxDepth must be a non-negative safe integer",
		)
	}
	return nil
}

func cloneAgentOptions(source *agent.Options) *agent.Options {
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

func cloneToolRestriction(
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

func cloneString(source *string) *string {
	if source == nil {
		return nil
	}
	snapshot := *source
	return &snapshot
}

func cloneInt64(source *int64) *int64 {
	if source == nil {
		return nil
	}
	snapshot := *source
	return &snapshot
}

func nilInterface(candidate any) bool {
	reflected := reflect.ValueOf(candidate)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
