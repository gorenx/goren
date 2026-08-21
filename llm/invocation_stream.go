package llm

import (
	"context"
	"sync"

	"github.com/gorenx/goren/plugin"
)

// invocationChunkStream binds a lazy Adapter stream to the complete Runtime
// Waterfall invocation that created it.
type invocationChunkStream struct {
	upstream ChunkStream
	lease    *plugin.InvocationLease

	finishOnce sync.Once
	mutex      sync.Mutex
	finished   bool
	stopCancel func() bool
	closeErr   error
}

func retainInvocationStream(
	upstream ChunkStream,
	lease *plugin.InvocationLease,
) ChunkStream {
	owned := &invocationChunkStream{
		upstream: upstream,
		lease:    lease,
	}
	stopCancel := context.AfterFunc(
		lease.Context(), func() {
			_ = owned.finish(context.Background())
		},
	)
	owned.mutex.Lock()
	if owned.finished {
		owned.mutex.Unlock()
		stopCancel()
		return owned
	}
	owned.stopCancel = stopCancel
	owned.mutex.Unlock()
	return owned
}

func (owned *invocationChunkStream) Next(
	requestContext context.Context,
) (StreamChunk, bool, error) {
	entry, available, err := owned.upstream.Next(requestContext)
	if err != nil || !available || (entry != nil && entry.ChunkType() == "finish") {
		_ = owned.finish(context.Background())
	}
	return entry, available, err
}

func (owned *invocationChunkStream) Close(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	return owned.finish(closeContext)
}

func (owned *invocationChunkStream) finish(closeContext context.Context) error {
	owned.finishOnce.Do(func() {
		owned.mutex.Lock()
		owned.finished = true
		stopCancel := owned.stopCancel
		owned.mutex.Unlock()
		if stopCancel != nil {
			stopCancel()
		}
		owned.closeErr = owned.upstream.Close(closeContext)
		owned.lease.Release()
	})
	return owned.closeErr
}
