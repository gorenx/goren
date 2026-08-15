package llm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/gorenx/goren/plugin"
)

type resolvedCall struct {
	config  CallConfig
	context *ModelContext
}

type preparedCallState struct {
	owner           *runtimeService
	route           *adapterRoute
	config          CallConfig
	policy          RetryPolicy
	context         *ModelContext
	adapterDefaults CallConfigAdapterDefaults

	mu         sync.Mutex
	dispatched bool
}

type normalizedChunkStream struct {
	upstream ChunkStream

	mu         sync.Mutex
	terminated bool
	closed     bool
}

func (owner *runtimeService) RetryPolicyFor(providerRoute string) (RetryPolicy, error) {
	record, err := owner.routeFor(providerRoute)
	if err != nil {
		return nil, err
	}
	return record.policy.CloneRetryPolicy(), nil
}

func (owner *runtimeService) ListModels(requestContext context.Context, providerRoute string) ([]ModelInfo, error) {
	record, err := owner.routeFor(providerRoute)
	if err != nil {
		return nil, err
	}
	catalog, ok := record.backend.(ModelCatalog)
	if !ok {
		return []ModelInfo{}, nil
	}
	models, err := catalog.ListModels(requestContext, providerRoute)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(models))
	result := make([]ModelInfo, 0, len(models))
	for _, candidate := range models {
		if candidate.Provider != providerRoute || candidate.ID == "" || candidate.Name == "" {
			return nil, MustLlmError(fmt.Sprintf("adapter returned invalid model metadata for provider %q", providerRoute), "INVALID_CATALOG")
		}
		if _, exists := seen[candidate.ID]; exists {
			return nil, MustLlmError(fmt.Sprintf("adapter returned duplicate model metadata for provider %q", providerRoute), "INVALID_CATALOG")
		}
		seen[candidate.ID] = struct{}{}
		candidate.InputModalities = slices.Clone(candidate.InputModalities)
		result = append(result, candidate)
	}
	return result, nil
}

func (owner *runtimeService) ResolveModelInfo(
	requestContext context.Context,
	providerRoute string,
	modelID string,
) (ResolvedModelInfo, error) {
	record, err := owner.routeFor(providerRoute)
	if err != nil {
		return ResolvedModelInfo{}, err
	}
	return owner.resolveModelFor(requestContext, record, modelID)
}

func (owner *runtimeService) resolveModelFor(
	requestContext context.Context,
	record *adapterRoute,
	modelID string,
) (ResolvedModelInfo, error) {
	providerRoute := record.metadata.ID
	resolved := ResolvedModelInfo{ModelInfo: ModelInfo{Provider: providerRoute, ID: modelID, Name: modelID}}
	if resolver, ok := record.backend.(ModelResolver); ok {
		candidate, err := resolver.ResolveModel(requestContext, providerRoute, modelID)
		if err != nil {
			return ResolvedModelInfo{}, err
		}
		resolved = candidate
	}
	if resolved.Provider != providerRoute || resolved.ID != modelID || resolved.Name == "" {
		return ResolvedModelInfo{}, MustLlmError(
			fmt.Sprintf("adapter returned invalid exact model metadata for provider %q model %q", providerRoute, modelID),
			"INVALID_MODEL_INFO",
		)
	}
	resolved.InputModalities = slices.Clone(resolved.InputModalities)
	if resolved.Context != nil {
		if resolved.Context.ContextWindow <= 0 {
			return ResolvedModelInfo{}, MustLlmError(
				fmt.Sprintf("adapter returned invalid context metadata for provider %q model %q", providerRoute, modelID),
				"INVALID_MODEL_CONTEXT",
			)
		}
		contextCopy := *resolved.Context
		resolved.Context = &contextCopy
	}
	if resolved.DefaultMaxTokens != nil {
		if *resolved.DefaultMaxTokens <= 0 {
			return ResolvedModelInfo{}, MustLlmError(
				fmt.Sprintf("adapter returned invalid default maxTokens for provider %q model %q", providerRoute, modelID),
				"INVALID_MODEL_MAX_TOKENS",
			)
		}
		resolved.DefaultMaxTokens = cloneInt(resolved.DefaultMaxTokens)
	}
	if resolved.Reasoning == nil {
		return resolved, nil
	}
	if len(resolved.Reasoning.Efforts) == 0 {
		return ResolvedModelInfo{}, MustLlmError(
			fmt.Sprintf("adapter returned invalid reasoning metadata for provider %q model %q", providerRoute, modelID),
			"INVALID_MODEL_REASONING",
		)
	}
	seen := make(map[ReasoningEffortID]struct{}, len(resolved.Reasoning.Efforts))
	efforts := make([]ReasoningEffortInfo, 0, len(resolved.Reasoning.Efforts))
	for _, effort := range resolved.Reasoning.Efforts {
		if effort.ID == "" || effort.Name == "" {
			return ResolvedModelInfo{}, MustLlmError(
				fmt.Sprintf("adapter returned invalid reasoning metadata for provider %q model %q", providerRoute, modelID),
				"INVALID_MODEL_REASONING",
			)
		}
		if _, exists := seen[effort.ID]; exists {
			return ResolvedModelInfo{}, MustLlmError(
				fmt.Sprintf("adapter returned duplicate reasoning metadata for provider %q model %q", providerRoute, modelID),
				"INVALID_MODEL_REASONING",
			)
		}
		seen[effort.ID] = struct{}{}
		efforts = append(efforts, effort)
	}
	if resolved.Reasoning.DefaultEffort != "" {
		if _, exists := seen[resolved.Reasoning.DefaultEffort]; !exists {
			return ResolvedModelInfo{}, MustLlmError(
				fmt.Sprintf("adapter returned an unknown default reasoning effort for provider %q model %q", providerRoute, modelID),
				"INVALID_MODEL_REASONING",
			)
		}
	}
	reasoningCopy := *resolved.Reasoning
	reasoningCopy.Efforts = efforts
	resolved.Reasoning = &reasoningCopy
	return resolved, nil
}

func (owner *runtimeService) ResolveCallConfig(requestContext context.Context, proposed CallConfig) (CallConfig, error) {
	record, err := owner.routeFor(proposed.Provider)
	if err != nil {
		return CallConfig{}, err
	}
	resolved, err := owner.resolveCallFor(requestContext, record, proposed)
	if err != nil {
		return CallConfig{}, err
	}
	return cloneCallConfig(resolved.config), nil
}

func (owner *runtimeService) resolveCallFor(
	requestContext context.Context,
	record *adapterRoute,
	proposed CallConfig,
) (resolvedCall, error) {
	if proposed.Provider == "" || proposed.Model == "" {
		return resolvedCall{}, MustLlmError("an LLM call needs a provider and model", "INVALID_ARGS")
	}
	metadata, err := owner.resolveModelFor(requestContext, record, proposed.Model)
	if err != nil {
		return resolvedCall{}, err
	}
	effective := cloneCallConfig(proposed)
	if effective.MaxTokens == nil && metadata.DefaultMaxTokens != nil {
		effective.MaxTokens = cloneInt(metadata.DefaultMaxTokens)
	}
	if metadata.Reasoning == nil {
		if effective.ReasoningEffort != "" {
			return resolvedCall{}, MustLlmError(
				fmt.Sprintf("provider %q model %q does not support reasoning effort %q", proposed.Provider, proposed.Model, effective.ReasoningEffort),
				"UNSUPPORTED_REASONING_EFFORT",
			)
		}
	} else {
		effortID := effective.ReasoningEffort
		if effortID == "" {
			effortID = metadata.Reasoning.DefaultEffort
		}
		if effortID != "" {
			matched := slices.ContainsFunc(metadata.Reasoning.Efforts, func(candidate ReasoningEffortInfo) bool {
				return candidate.ID == effortID
			})
			if !matched {
				return resolvedCall{}, MustLlmError(
					fmt.Sprintf("provider %q model %q does not support reasoning effort %q", proposed.Provider, proposed.Model, effortID),
					"UNSUPPORTED_REASONING_EFFORT",
				)
			}
			effective.ReasoningEffort = effortID
		}
	}
	result := resolvedCall{config: effective}
	if metadata.Context != nil {
		contextCopy := *metadata.Context
		result.context = &contextCopy
	}
	return result, nil
}

func (owner *runtimeService) PrepareCall(requestContext context.Context, proposed CallConfig) (PreparedLlmCall, error) {
	record, err := owner.routeFor(proposed.Provider)
	if err != nil {
		return nil, err
	}
	resolved, err := owner.resolveCallFor(requestContext, record, proposed)
	if err != nil {
		return nil, err
	}
	defaults := CallConfigAdapterDefaults{
		ReasoningEffort: proposed.ReasoningEffort == "" && resolved.config.ReasoningEffort != "",
		MaxTokens:       proposed.MaxTokens == nil && resolved.config.MaxTokens != nil,
	}
	return &preparedCallState{
		owner: owner, route: record, config: cloneCallConfig(resolved.config),
		policy: record.policy.CloneRetryPolicy(), context: cloneModelContext(resolved.context),
		adapterDefaults: defaults,
	}, nil
}

func (prepared *preparedCallState) ConfigValue() CallConfig {
	return cloneCallConfig(prepared.config)
}

func (prepared *preparedCallState) RetryPolicyValue() RetryPolicy {
	return prepared.policy.CloneRetryPolicy()
}

func (prepared *preparedCallState) ContextValue() (ModelContext, bool) {
	if prepared.context == nil {
		return ModelContext{}, false
	}
	return *prepared.context, true
}

func (prepared *preparedCallState) AdapterDefaultsValue() CallConfigAdapterDefaults {
	return prepared.adapterDefaults
}

func (prepared *preparedCallState) Stream(requestContext context.Context, options GenerateOptions) (ChunkStream, error) {
	prepared.mu.Lock()
	if prepared.dispatched {
		prepared.mu.Unlock()
		return nil, MustLlmError("a prepared LLM call can only be dispatched once", "INVALID_PREPARED_CALL")
	}
	if !callConfigEqual(options.CallConfig, prepared.config) {
		prepared.mu.Unlock()
		return nil, MustLlmError("prepared LLM call config changed before adapter dispatch", "INVALID_PREPARED_CALL")
	}
	prepared.dispatched = true
	prepared.mu.Unlock()
	return prepared.owner.streamWithRoute(requestContext, options, prepared.route, &prepared.config)
}

func (owner *runtimeService) Stream(requestContext context.Context, options GenerateOptions) (ChunkStream, error) {
	return owner.streamWithRoute(requestContext, options, nil, nil)
}

func (owner *runtimeService) streamWithRoute(
	requestContext context.Context,
	options GenerateOptions,
	record *adapterRoute,
	preparedConfig *CallConfig,
) (ChunkStream, error) {
	detached, err := cloneGenerateOptions(options)
	if err != nil {
		return nil, err
	}
	return plugin.WaterfallFrom(requestContext, owner.sourceScope, StreamEvent, detached,
		func(chainContext context.Context, request GenerateOptions) (ChunkStream, error) {
			return owner.adapterBoundary(chainContext, request, record, preparedConfig)
		})
}

func (owner *runtimeService) adapterBoundary(
	requestContext context.Context,
	options GenerateOptions,
	record *adapterRoute,
	preparedConfig *CallConfig,
) (ChunkStream, error) {
	selected := record
	if selected == nil {
		var err error
		selected, err = owner.routeFor(options.Provider)
		if err != nil {
			return newTerminalStream(requestContext, err)
		}
	}
	var effective CallConfig
	if preparedConfig == nil {
		resolved, err := owner.resolveCallFor(requestContext, selected, options.CallConfig)
		if err != nil {
			return newTerminalStream(requestContext, err)
		}
		effective = resolved.config
	} else {
		if !callConfigEqual(options.CallConfig, *preparedConfig) {
			return newTerminalStream(requestContext, MustLlmError(
				"prepared LLM call config changed before adapter dispatch", "INVALID_PREPARED_CALL",
			))
		}
		effective = cloneCallConfig(*preparedConfig)
	}
	detached, err := cloneGenerateOptions(options)
	if err != nil {
		return newTerminalStream(requestContext, err)
	}
	detached.CallConfig = effective
	detached, err = owner.forAdapter(detached, selected.backend)
	if err != nil {
		return newTerminalStream(requestContext, err)
	}
	upstream, err := selected.backend.Stream(requestContext, detached)
	if err != nil {
		return newTerminalStream(requestContext, err)
	}
	if upstream == nil {
		return newTerminalStream(requestContext, errors.New("llm: adapter returned a nil stream"))
	}
	return &normalizedChunkStream{upstream: upstream}, nil
}

func (owner *runtimeService) forAdapter(options GenerateOptions, backend Adapter) (GenerateOptions, error) {
	changed := false
	conversation := make([]Message, len(options.Messages))
	for index, entry := range options.Messages {
		conversation[index] = entry
		if entry.ConversationRole() != RoleAssistant {
			continue
		}
		origin := entry.SourceValue()
		modelOrigin, ok := origin.(ModelMessageSource)
		if !ok || len(modelOrigin.ReplayState) == 0 {
			continue
		}
		owner.mu.RLock()
		historical := owner.routes[modelOrigin.Provider]
		preserve := historical != nil && historical.backend == backend
		owner.mu.RUnlock()
		if preserve {
			continue
		}
		modelOrigin.ReplayState = nil
		restored, err := restoreMessageValue(
			entry.StableID(), entry.ConversationRole(), entry.ContentValue(), modelOrigin,
		)
		if err != nil {
			return GenerateOptions{}, err
		}
		conversation[index] = restored
		changed = true
	}
	if changed {
		options.Messages = conversation
	}
	return options, nil
}

func (owner *runtimeService) routeFor(providerRoute string) (*adapterRoute, error) {
	owner.mu.RLock()
	record := owner.routes[providerRoute]
	owner.mu.RUnlock()
	if record == nil {
		return nil, MustLlmError(fmt.Sprintf("no adapter registered for provider %q", providerRoute), "NO_ADAPTER")
	}
	return record, nil
}

func newTerminalStream(requestContext context.Context, problem error) (ChunkStream, error) {
	failureSnapshot := normalizeLlmFailure(problem)
	var reason FinishReason = ErrorFinish{Kind: "error", Failure: failureSnapshot}
	if requestContext.Err() != nil || failureSnapshot.Code == "ABORTED" {
		reason = AbortedFinish{Kind: "aborted", Failure: failureSnapshot}
	}
	return NewSliceStream([]StreamChunk{FinishChunk{Type: "finish", Reason: reason}})
}

func (flow *normalizedChunkStream) Next(requestContext context.Context) (StreamChunk, bool, error) {
	flow.mu.Lock()
	if flow.closed || flow.terminated {
		flow.mu.Unlock()
		return nil, false, nil
	}
	flow.mu.Unlock()
	entry, present, err := flow.upstream.Next(requestContext)
	if err != nil {
		flow.mu.Lock()
		flow.terminated = true
		flow.mu.Unlock()
		terminal, terminalErr := newTerminalStream(requestContext, err)
		if terminalErr != nil {
			return nil, false, terminalErr
		}
		return terminal.Next(requestContext)
	}
	if !present {
		flow.mu.Lock()
		flow.terminated = true
		flow.mu.Unlock()
		return nil, false, nil
	}
	if entry == nil {
		flow.mu.Lock()
		flow.terminated = true
		flow.mu.Unlock()
		terminal, terminalErr := newTerminalStream(requestContext, errors.New("llm: adapter yielded a nil stream chunk"))
		if terminalErr != nil {
			return nil, false, terminalErr
		}
		return terminal.Next(requestContext)
	}
	if entry.ChunkType() == "finish" {
		flow.mu.Lock()
		flow.terminated = true
		flow.mu.Unlock()
	}
	return entry, true, nil
}

func (flow *normalizedChunkStream) Close(closeContext context.Context) error {
	flow.mu.Lock()
	if flow.closed {
		flow.mu.Unlock()
		return nil
	}
	flow.closed = true
	flow.mu.Unlock()
	return flow.upstream.Close(closeContext)
}

func cloneModelContext(source *ModelContext) *ModelContext {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}
