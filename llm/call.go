package llm

import (
	"context"
	"errors"
	"slices"

	"github.com/gorenx/goren/agentmessage"
)

// Streamer is the stream-opening operation available to a stream middleware.
type Streamer interface {
	Stream(context.Context, GenerateOptions) (ChunkStream, error)
}

// StreamMiddleware may wrap or replace a stream. Its failure remains an error,
// while failures from the terminal provider are represented by finish chunks.
type StreamMiddleware interface {
	Intercept(context.Context, GenerateOptions, Streamer) (ChunkStream, error)
}

// Call fixes one model dispatch independently of registry mutations.
// A worker serializes its use and retains the resulting stream until Close succeeds.
type Call struct {
	binding    *Binding
	config     CallConfig
	defaults   CallConfigAdapterDefaults
	capacity   *ModelContext
	middleware []StreamMiddleware
	dispatched bool
	stream     ChunkStream
	closed     bool
}

// NewCall resolves exact model metadata before any stream is opened.
// A missing adapter still permits middleware to supply a replacement stream.
// Only the terminal reports NO_ADAPTER; invalid configuration fails preparation.
func NewCall(requestContext context.Context, selected *Binding, proposed CallConfig, middleware []StreamMiddleware) (*Call, error) {
	if proposed.Provider == "" || proposed.Model == "" {
		return nil, MustLlmError("model call requires provider and model", "INVALID_ARGS")
	}
	for _, handler := range middleware {
		if handler == nil {
			return nil, errors.New("llm: nil stream middleware")
		}
	}
	prepared := &Call{
		binding:    selected,
		config:     cloneCallConfig(proposed),
		middleware: slices.Clone(middleware),
	}
	if selected == nil {
		return prepared, nil
	}
	resolved, err := selected.resolveCall(requestContext, proposed)
	if err != nil {
		return nil, err
	}
	prepared.config = resolved.config
	prepared.capacity = cloneModelContext(resolved.context)
	prepared.defaults = CallConfigAdapterDefaults{
		ReasoningEffort: proposed.ReasoningEffort == "" && resolved.config.ReasoningEffort != "",
		MaxTokens:       proposed.MaxTokens == nil && resolved.config.MaxTokens != nil,
	}
	return prepared, nil
}

// ConfigValue returns a detached effective configuration.
func (prepared *Call) ConfigValue() CallConfig { return cloneCallConfig(prepared.config) }

// ContextValue returns exact model capacity when advertised.
func (prepared *Call) ContextValue() (ModelContext, bool) {
	if prepared.capacity == nil {
		return ModelContext{}, false
	}
	return *prepared.capacity, true
}

// AdapterDefaultsValue identifies the defaults applied by this adapter.
func (prepared *Call) AdapterDefaultsValue() CallConfigAdapterDefaults { return prepared.defaults }

// RetryPolicyValue returns the selected registration's policy, if any.
func (prepared *Call) RetryPolicyValue() RetryPolicy {
	if prepared.binding == nil {
		return nil
	}
	return prepared.binding.policy.CloneRetryPolicy()
}

// openStream dispatches through the captured middleware and terminal adapter.
func (prepared *Call) openStream(requestContext context.Context, options GenerateOptions) (ChunkStream, error) {
	if prepared.dispatched || prepared.closed {
		return nil, MustLlmError("a model call can only be dispatched once", "INVALID_PREPARED_CALL")
	}
	if !callConfigEqual(options.CallConfig, prepared.config) {
		return nil, MustLlmError("prepared model configuration changed", "INVALID_PREPARED_CALL")
	}
	detached, err := cloneGenerateOptions(options)
	if err != nil {
		return nil, err
	}
	prepared.dispatched = true
	endpoint := &callTerminal{
		call: prepared,
	}
	var terminal Streamer = endpoint
	for index := len(prepared.middleware) - 1; index >= 0; index-- {
		terminal = streamHandler{
			handler: prepared.middleware[index],
			next:    terminal,
		}
	}
	opened, err := terminal.Stream(requestContext, detached)
	if opened == nil {
		if err == nil {
			err = errors.New("llm: stream middleware returned no stream")
		}
		// A middleware may fail after opening the terminal. Preserve that
		// resource even when the middleware no longer returns its wrapper.
		return endpoint.acquired, err
	}
	return opened, err
}

type streamHandler struct {
	handler StreamMiddleware
	next    Streamer
}

func (handler streamHandler) Stream(requestContext context.Context, options GenerateOptions) (ChunkStream, error) {
	return handler.handler.Intercept(requestContext, options, handler.next)
}

type callTerminal struct {
	call     *Call
	invoked  bool
	acquired ChunkStream
}

func (terminal *callTerminal) Stream(requestContext context.Context, options GenerateOptions) (ChunkStream, error) {
	if terminal.invoked {
		return nil, MustLlmError("terminal adapter can only be invoked once", "INVALID_PREPARED_CALL")
	}
	terminal.invoked = true
	prepared := terminal.call
	if !callConfigEqual(options.CallConfig, prepared.config) {
		return newTerminalStream(requestContext, MustLlmError("prepared model configuration changed in middleware", "INVALID_PREPARED_CALL"))
	}
	if prepared.binding == nil {
		return newTerminalStream(requestContext, MustLlmError("no adapter selected for model call", "NO_ADAPTER"))
	}
	detached, err := cloneGenerateOptions(options)
	if err != nil {
		return newTerminalStream(requestContext, err)
	}
	detached, err = prepared.binding.forAdapter(detached)
	if err != nil {
		return newTerminalStream(requestContext, err)
	}
	upstream, err := prepared.binding.backend.Stream(requestContext, detached)
	if err != nil {
		finish, failure := newTerminalStream(requestContext, err)
		if upstream != nil {
			retained := &failedStream{
				terminal: finish,
				upstream: upstream,
			}
			terminal.acquired = retained
			return retained, failure
		}
		return finish, failure
	}
	if upstream == nil {
		return newTerminalStream(requestContext, errors.New("llm: adapter returned no stream"))
	}
	retained := &normalizedChunkStream{
		upstream: upstream,
	}
	terminal.acquired = retained
	return retained, nil
}

// failedStream reports opening failure but retains any partially acquired resource.
type failedStream struct {
	terminal ChunkStream
	upstream ChunkStream
}

func (flow *failedStream) Next(requestContext context.Context) (StreamChunk, bool, error) {
	return flow.terminal.Next(requestContext)
}
func (flow *failedStream) Close(closeContext context.Context) error {
	return flow.upstream.Close(closeContext)
}

func (record *Binding) forAdapter(options GenerateOptions) (GenerateOptions, error) {
	conversation := make([]agentmessage.Message, len(options.Messages))
	for index, entry := range options.Messages {
		conversation[index] = entry
		if entry.ConversationRole() != agentmessage.RoleAssistant {
			continue
		}
		origin, ok := entry.SourceValue().(agentmessage.ModelMessageSource)
		if !ok || len(origin.ReplayState) == 0 || slices.Contains(record.replayProviders, origin.Provider) {
			continue
		}
		origin.ReplayState = nil
		restored, err := agentmessage.ReplaceSource(entry, origin)
		if err != nil {
			return GenerateOptions{}, err
		}
		conversation[index] = restored
	}
	options.Messages = conversation
	return options, nil
}

// Open acquires one stream and retains partial resources even when opening fails.
func (prepared *Call) Open(requestContext context.Context, options GenerateOptions) error {
	if prepared == nil {
		return errors.New("llm: model call has not been prepared")
	}
	if requestContext.Err() != nil {
		return context.Cause(requestContext)
	}
	opened, err := prepared.openStream(requestContext, options)
	if opened != nil {
		prepared.stream = opened
	}
	if requestContext.Err() != nil {
		return context.Cause(requestContext)
	}
	return err
}

// Next reads one detached chunk; cancellation never claims that resources closed.
func (prepared *Call) Next(requestContext context.Context) (StreamChunk, bool, error) {
	if prepared == nil || prepared.stream == nil {
		return nil, false, errors.New("llm: model stream is not open")
	}
	if requestContext.Err() != nil {
		return nil, false, context.Cause(requestContext)
	}
	chunk, found, err := prepared.stream.Next(requestContext)
	if err == nil && found {
		if chunk == nil {
			err = errors.New("llm: empty stream chunk")
		} else {
			chunk, err = chunk.CloneChunk()
		}
	}
	if requestContext.Err() != nil {
		err = context.Cause(requestContext)
	}
	return chunk, found, err
}

// Close retains the stream on failure and confirms release only after success.
// The caller supplies a cleanup context independent of generation cancellation.
func (prepared *Call) Close(closeContext context.Context) error {
	if prepared == nil || prepared.closed {
		return nil
	}
	if prepared.stream != nil {
		if err := prepared.stream.Close(closeContext); err != nil {
			return err
		}
	}
	prepared.stream = nil
	prepared.closed = true
	return nil
}
