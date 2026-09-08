package loopactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/llm"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// streamReader owns one model stream. Its mailbox serializes I/O while the
// AgentLoop remains free to process cancellation and other lifecycle messages.
type streamReader struct {
	stream    llm.ChunkStream
	operation context.Context
}

type openStream struct {
	operation context.Context
	call      llm.PreparedLlmCall
	options   llm.GenerateOptions
}

type streamOpened struct {
	err error
}

type readChunk struct{}

type chunkRead struct {
	chunk llm.StreamChunk
	found bool
	err   error
}

type closeStream struct{}

type streamClosed struct {
	err error
}

func (*streamReader) PreStart(*goakt.Context) error { return nil }

// PostStop releases an interrupted attempt without asserting successful closure
// when the provider reports a failure.
func (reader *streamReader) PostStop(*goakt.Context) error {
	return reader.close()
}

func (reader *streamReader) Receive(rc *goakt.ReceiveContext) {
	switch input := rc.Message().(type) {
	case openStream:
		rc.Response(streamOpened{
			err: reader.open(input),
		})
	case readChunk:
		result := chunkRead{}
		if reader.stream == nil {
			result.err = errors.New("agentloop: model stream is not open")
		} else if reader.operation.Err() != nil {
			result.err = context.Cause(reader.operation)
		} else {
			result.chunk, result.found, result.err = reader.stream.Next(reader.operation)
			if result.err == nil && result.found {
				if result.chunk == nil {
					result.err = errors.New("agentloop: provider returned an empty stream chunk")
				} else {
					result.chunk, result.err = result.chunk.CloneChunk()
				}
			}
			if reader.operation.Err() != nil {
				result.err = context.Cause(reader.operation)
			}
		}
		rc.Response(result)
	case closeStream:
		rc.Response(streamClosed{
			err: reader.close(),
		})
	default:
		rc.Unhandled()
	}
}

func (reader *streamReader) open(input openStream) error {
	if reader.stream != nil {
		return errors.New("agentloop: previous model stream has not closed")
	}
	if input.operation == nil || input.call == nil {
		return errors.New("agentloop: prepared model call and operation context are required")
	}
	if input.operation.Err() != nil {
		return context.Cause(input.operation)
	}
	detached, err := llm.CloneGenerateOptions(input.options)
	if err != nil {
		return err
	}
	opened, err := input.call.Stream(input.operation, detached)
	// Retain any acquired stream even when opening also reports an error. The
	// owner must close it before retrying or releasing this worker.
	reader.stream = opened
	reader.operation = input.operation
	if input.operation.Err() != nil {
		return context.Cause(input.operation)
	}
	if err != nil {
		return err
	}
	if opened == nil {
		return errors.New("agentloop: provider returned no model stream")
	}
	return nil
}

func (reader *streamReader) close() error {
	if reader.stream == nil {
		return nil
	}
	if err := reader.stream.Close(context.WithoutCancel(reader.operation)); err != nil {
		return err
	}
	reader.stream = nil
	reader.operation = nil
	return nil
}
