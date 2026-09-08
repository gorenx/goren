package llm

import (
	"context"
	"errors"
)

// normalizedChunkStream contains only terminal adapter failures, not middleware failures.
// The invocation worker serializes Next and Close; failed Close remains retryable.
type normalizedChunkStream struct {
	upstream   ChunkStream
	terminated bool
	closed     bool
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
	if flow.closed || flow.terminated {
		return nil, false, nil
	}
	entry, present, err := flow.upstream.Next(requestContext)
	if err != nil {
		flow.terminated = true
		terminal, terminalErr := newTerminalStream(requestContext, err)
		if terminalErr != nil {
			return nil, false, terminalErr
		}
		return terminal.Next(requestContext)
	}
	if !present {
		flow.terminated = true
		return nil, false, nil
	}
	if entry == nil {
		flow.terminated = true
		terminal, terminalErr := newTerminalStream(requestContext, errors.New("llm: adapter yielded a nil stream chunk"))
		if terminalErr != nil {
			return nil, false, terminalErr
		}
		return terminal.Next(requestContext)
	}
	if entry.ChunkType() == "finish" {
		flow.terminated = true
	}
	return entry, true, nil
}

func (flow *normalizedChunkStream) Close(closeContext context.Context) error {
	if flow.closed {
		return nil
	}
	if err := flow.upstream.Close(closeContext); err != nil {
		return err
	}
	flow.closed = true
	return nil
}
