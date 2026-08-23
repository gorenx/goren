package deepseek

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/llm"
)

type deepSeekStream struct {
	backend        *Adapter
	requestOptions llm.GenerateOptions

	operationContext context.Context
	cancelOperation  context.CancelFunc

	nextMu      sync.Mutex
	mu          sync.Mutex
	initialized bool
	closed      bool
	terminated  bool
	downstream  llm.ChunkStream
	baseURL     string
}

func (streamState *deepSeekStream) Next(requestContext context.Context) (llm.StreamChunk, bool, error) {
	if requestContext == nil {
		requestContext = context.Background()
	}
	streamState.nextMu.Lock()
	defer streamState.nextMu.Unlock()
	if err := requestContext.Err(); err != nil {
		streamState.cancelOperation()
		return nil, false, abortedError(err)
	}
	streamState.mu.Lock()
	if streamState.closed {
		streamState.mu.Unlock()
		return nil, false, nil
	}
	initialized := streamState.initialized
	terminated := streamState.terminated
	streamState.mu.Unlock()
	if terminated {
		return nil, false, nil
	}
	if !initialized {
		if err := streamState.initialize(requestContext); err != nil {
			streamState.markTerminated()
			streamState.cancelOperation()
			return nil, false, err
		}
	}
	entry, available, err := streamState.downstream.Next(requestContext)
	if err == nil {
		if !available || (entry != nil && entry.ChunkType() == "finish") {
			streamState.markTerminated()
			streamState.cancelOperation()
		}
		return entry, available, nil
	}
	normalized := streamState.normalizeStreamError(requestContext, err)
	streamState.markTerminated()
	streamState.cancelOperation()
	return nil, false, normalized
}

func (streamState *deepSeekStream) initialize(requestContext context.Context) error {
	downstream, baseURL, err := streamState.backend.openStream(
		requestContext,
		streamState.operationContext,
		streamState.cancelOperation,
		streamState.requestOptions,
	)
	if err != nil {
		return err
	}
	streamState.mu.Lock()
	streamState.initialized = true
	streamState.downstream = downstream
	streamState.baseURL = baseURL
	streamState.mu.Unlock()
	return nil
}

func (streamState *deepSeekStream) normalizeStreamError(requestContext context.Context, err error) error {
	var providerFailure *llm.LlmError
	if errors.As(err, &providerFailure) {
		return providerFailure
	}
	if requestContext.Err() != nil || streamState.operationContext.Err() != nil {
		streamState.cancelOperation()
		return abortedError(err)
	}
	return llm.MustLlmError(
		fmt.Sprintf("DeepSeek API stream from %s failed", streamState.baseURL),
		"TRANSPORT",
		llm.LlmErrorOptions{
			Cause: err,
		},
	)
}

func (streamState *deepSeekStream) Close(closeContext context.Context) error {
	streamState.mu.Lock()
	if streamState.closed {
		streamState.mu.Unlock()
		return nil
	}
	streamState.closed = true
	downstream := streamState.downstream
	streamState.mu.Unlock()
	streamState.cancelOperation()
	if downstream != nil {
		return downstream.Close(closeContext)
	}
	return nil
}

func (streamState *deepSeekStream) markTerminated() {
	streamState.mu.Lock()
	streamState.terminated = true
	streamState.mu.Unlock()
}
