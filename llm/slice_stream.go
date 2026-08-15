package llm

import (
	"context"
	"fmt"
	"sync"
)

// sliceChunkStream is the deterministic in-process stream used by tests and
// scripted adapters; it is not part of adapter normalization.
type sliceChunkStream struct {
	mu      sync.Mutex
	entries []StreamChunk
	index   int
	closed  bool
}

// NewSliceStream snapshots a deterministic stream for tests and in-process adapters.
func NewSliceStream(entries []StreamChunk) (ChunkStream, error) {
	detached := make([]StreamChunk, len(entries))
	for index, entry := range entries {
		if entry == nil {
			return nil, fmt.Errorf("llm: stream chunk %d is nil", index)
		}
		copyValue, err := entry.CloneChunk()
		if err != nil {
			return nil, fmt.Errorf("llm: clone stream chunk %d: %w", index, err)
		}
		detached[index] = copyValue
	}
	return &sliceChunkStream{entries: detached}, nil
}

func (streamState *sliceChunkStream) Next(requestContext context.Context) (StreamChunk, bool, error) {
	if err := requestContext.Err(); err != nil {
		return nil, false, err
	}
	streamState.mu.Lock()
	defer streamState.mu.Unlock()
	if streamState.closed || streamState.index >= len(streamState.entries) {
		return nil, false, nil
	}
	entry := streamState.entries[streamState.index]
	streamState.index++
	copyValue, err := entry.CloneChunk()
	return copyValue, true, err
}

func (streamState *sliceChunkStream) Close(context.Context) error {
	streamState.mu.Lock()
	streamState.closed = true
	streamState.mu.Unlock()
	return nil
}
