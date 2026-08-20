package llm

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/plugin"
)

// normalizedChunkStream contains adapter stream failures and enforces the
// first-terminal-chunk boundary exposed by LlmRuntime.
type normalizedChunkStream struct {
	upstream ChunkStream

	mu         sync.Mutex
	terminated bool
	closed     bool
}

func (owner *Runtime) Stream(requestContext context.Context, options GenerateOptions) (ChunkStream, error) {
	return owner.streamWithRoute(requestContext, options, nil, nil)
}

func (owner *Runtime) streamWithRoute(
	requestContext context.Context,
	options GenerateOptions,
	record *adapterRoute,
	preparedConfig *CallConfig,
) (ChunkStream, error) {
	detached, err := cloneGenerateOptions(options)
	if err != nil {
		return nil, err
	}
	result, err := plugin.Run(
		requestContext,
		owner,
		detached,
		streamTerminal{
			owner:          owner,
			record:         record,
			preparedConfig: preparedConfig,
		},
	)
	if err != nil {
		return nil, err
	}
	if result.Stream == nil {
		return nil, errors.New("llm: stream Waterfall returned a nil stream")
	}
	return result.Stream, nil
}

type streamTerminal struct {
	owner          *Runtime
	record         *adapterRoute
	preparedConfig *CallConfig
}

func (terminal streamTerminal) Execute(
	requestContext context.Context,
	options GenerateOptions,
) (StreamOutput, error) {
	generatedChunks, err := terminal.owner.adapterBoundary(
		requestContext,
		options,
		terminal.record,
		terminal.preparedConfig,
	)
	return StreamOutput{
		Stream: generatedChunks,
	}, err
}

func (owner *Runtime) adapterBoundary(
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
	return &normalizedChunkStream{
		upstream: upstream,
	}, nil
}

func (owner *Runtime) forAdapter(options GenerateOptions, backend Adapter) (GenerateOptions, error) {
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

func (owner *Runtime) routeFor(providerRoute string) (*adapterRoute, error) {
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
	var reason FinishReason = ErrorFinish{
		Kind:    "error",
		Failure: failureSnapshot,
	}
	if requestContext.Err() != nil || failureSnapshot.Code == "ABORTED" {
		reason = AbortedFinish{
			Kind:    "aborted",
			Failure: failureSnapshot,
		}
	}
	return NewSliceStream([]StreamChunk{
		FinishChunk{
			Type:   "finish",
			Reason: reason,
		},
	})
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
