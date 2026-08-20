package llm

import (
	"context"
	"sync"
)

// preparedCallState is the immutable adapter/model snapshot for one dispatch.
// Its mutex protects the single-use transition, not model or route discovery.
type preparedCallState struct {
	owner           *Runtime
	route           *adapterRoute
	config          CallConfig
	policy          RetryPolicy
	context         *ModelContext
	adapterDefaults CallConfigAdapterDefaults

	mu         sync.Mutex
	dispatched bool
}

func (owner *Runtime) PrepareCall(requestContext context.Context, proposed CallConfig) (PreparedLlmCall, error) {
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
