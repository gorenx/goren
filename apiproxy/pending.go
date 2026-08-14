package apiproxy

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/connection"
)

// ErrPendingResponseWithdrawn is returned when an owner withdraws a pending
// interaction without supplying a more specific cause.
var ErrPendingResponseWithdrawn = errors.New("apiproxy: pending response withdrawn")

// ResponseDecoder performs the business-owned second parse for one pending
// interaction. Returning false rejects the response as bad-response while
// leaving the interaction pending so the client can retry.
type ResponseDecoder[V any] func(connection.RPCResult) (V, bool)

type pendingEntry struct {
	decode   func(connection.RPCResult) (any, bool, error)
	complete func(any, error)
}

// PendingResponse is the owner handle for one answerable ServerRequest. It
// survives carrier disconnects until a valid response wins or the owner
// explicitly withdraws it.
type PendingResponse[V any] struct {
	methods       *Catalog
	correlationID connection.RPCID
	entry         *pendingEntry
	completed     chan struct{}
	value         V
	err           error
}

// RegisterPendingResponse reserves a server-minted rpcId and installs the
// business response decoder that /api/respond will route to.
func RegisterPendingResponse[V any](methods *Catalog, correlationID connection.RPCID, decodeResponse ResponseDecoder[V]) (*PendingResponse[V], error) {
	if methods == nil {
		return nil, errors.New("apiproxy: catalog is nil")
	}
	if decodeResponse == nil {
		return nil, errors.New("apiproxy: pending response decoder is nil")
	}

	waiting := &PendingResponse[V]{
		methods: methods, correlationID: correlationID, completed: make(chan struct{}),
	}
	entry := &pendingEntry{
		decode: func(result connection.RPCResult) (any, bool, error) {
			value, valid, err := invokeResponseDecoder(decodeResponse, result)
			return value, valid, err
		},
		complete: func(value any, cause error) {
			if cause != nil {
				waiting.err = cause
			} else {
				waiting.value = value.(V)
			}
			close(waiting.completed)
		},
	}
	waiting.entry = entry

	methods.pendingMutex.Lock()
	defer methods.pendingMutex.Unlock()
	if _, exists := methods.pending[correlationID]; exists {
		return nil, fmt.Errorf("apiproxy: rpcId %q is already pending", correlationID)
	}
	methods.pending[correlationID] = entry
	return waiting, nil
}

// Wait blocks until a valid client response settles the interaction or the
// supplied context wins and withdraws it. If settlement already claimed the
// entry, Wait returns that authoritative result even when cancellation races.
func (waiting *PendingResponse[V]) Wait(waitContext context.Context) (V, error) {
	select {
	case <-waiting.completed:
		return waiting.value, waiting.err
	case <-waitContext.Done():
		if waiting.Withdraw(waitContext.Err()) {
			return waiting.value, waiting.err
		}
		<-waiting.completed
		return waiting.value, waiting.err
	}
}

// Withdraw removes this interaction if it is still pending. Late client
// responses then receive not-pending. The first claimant wins.
func (waiting *PendingResponse[V]) Withdraw(cause error) bool {
	if cause == nil {
		cause = ErrPendingResponseWithdrawn
	}
	methods := waiting.methods
	methods.pendingMutex.Lock()
	if methods.pending[waiting.correlationID] != waiting.entry {
		methods.pendingMutex.Unlock()
		return false
	}
	delete(methods.pending, waiting.correlationID)
	methods.pendingMutex.Unlock()
	waiting.entry.complete(nil, cause)
	return true
}

// Respond routes a ClientResponse by its echoed rpcId. Unknown, late, and
// duplicate answers are not-pending; an invalid business payload is
// bad-response and leaves the entry available for a corrected retry.
func (methods *Catalog) Respond(_ context.Context, message connection.ClientResponse) (connection.RPCReceipt, error) {
	methods.pendingMutex.Lock()
	entry := methods.pending[message.RPCID]
	methods.pendingMutex.Unlock()
	if entry == nil {
		return connection.RejectedReceipt(connection.ReceiptNotPending), nil
	}

	value, valid, err := entry.decode(message.Result)
	if err != nil {
		return connection.RPCReceipt{}, err
	}
	if !valid {
		return connection.RejectedReceipt(connection.ReceiptBadResponse), nil
	}

	methods.pendingMutex.Lock()
	if methods.pending[message.RPCID] != entry {
		methods.pendingMutex.Unlock()
		return connection.RejectedReceipt(connection.ReceiptNotPending), nil
	}
	delete(methods.pending, message.RPCID)
	methods.pendingMutex.Unlock()
	entry.complete(value, nil)
	return connection.AcceptedReceipt(), nil
}

func invokeResponseDecoder[V any](decodeResponse ResponseDecoder[V], result connection.RPCResult) (value V, valid bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	value, valid = decodeResponse(result)
	return value, valid, nil
}
