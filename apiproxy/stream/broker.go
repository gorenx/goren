package stream

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/gorenx/goren/connection"
)

var (
	// ErrClosed rejects subscriptions and publications after shutdown.
	ErrClosed = errors.New("apiproxy: stream broker is closed")
	// ErrSlowConsumer terminates one subscriber whose bounded delivery buffer filled.
	ErrSlowConsumer = errors.New("apiproxy: stream subscriber is too slow")
)

// Options configures bounded per-connection buffering and frame correlation.
type Options struct {
	Capacity int
	NewRPCID func() (connection.RPCID, error)
}

// Broker owns transport subscriptions and replayable interaction frames.
// It retains encoded payloads, never Agent, Session or Gateway objects.
type Broker struct {
	mu       sync.Mutex
	capacity int
	newRPC   func() (connection.RPCID, error)
	mux      map[*delivery]struct{}
	host     map[*delivery]struct{}
	pending  map[connection.RPCID]publication
	order    []connection.RPCID
	closed   bool
}

type publication struct {
	kind    string
	request connection.RPCRequest
}
type delivery struct {
	frames chan Batch
	// failure is assigned before closing frames and read only after it is drained.
	failure error
}

// Subscription is one connection's captured replay baseline and future frames.
// It is a single-reader handle. Its owner must call Broker.Release on exit.
type Subscription struct {
	incoming *delivery
	replay   []connection.RPCRequest
}

// NewBroker creates an empty broker; zero capacity selects 256 batches.
func NewBroker(settings Options) (*Broker, error) {
	if settings.Capacity < 0 {
		return nil, errors.New("apiproxy: negative stream capacity")
	}
	if settings.Capacity == 0 {
		settings.Capacity = 256
	}
	if settings.NewRPCID == nil {
		settings.NewRPCID = NewRPCID
	}
	return &Broker{
		capacity: settings.Capacity,
		newRPC:   settings.NewRPCID,
		mux:      make(map[*delivery]struct{}),
		host:     make(map[*delivery]struct{}),
		pending:  make(map[connection.RPCID]publication),
	}, nil
}

// OpenMux registers before capturing pending interactions, closing the replay/live gap.
func (hub *Broker) OpenMux() (*Subscription, error) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return nil, ErrClosed
	}
	source := &delivery{
		frames: make(chan Batch, hub.capacity),
	}
	hub.mux[source] = struct{}{}
	replay := make([]connection.RPCRequest, 0, len(hub.pending))
	for _, kind := range []string{"question/requested", "approval/requested"} {
		for _, id := range hub.order {
			if item, ok := hub.pending[id]; ok && item.kind == kind {
				replay = append(replay, cloneRequest(item.request))
			}
		}
	}
	return &Subscription{
		incoming: source,
		replay:   replay,
	}, nil
}

// OpenHost registers a live-only host feed; domains provide their own baselines.
func (hub *Broker) OpenHost() (*Subscription, error) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return nil, ErrClosed
	}
	source := &delivery{
		frames: make(chan Batch, hub.capacity),
	}
	hub.host[source] = struct{}{}
	return &Subscription{
		incoming: source,
	}, nil
}

// Replay returns detached pending frames captured when the subscription opened.
func (reader *Subscription) Replay() []connection.RPCRequest {
	result := make([]connection.RPCRequest, len(reader.replay))
	for i, item := range reader.replay {
		result[i] = cloneRequest(item)
	}
	return result
}

// Next returns one live frame, cancellation, or the terminal subscription result.
func (reader *Subscription) Next(ctx context.Context) (Batch, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	select {
	case item, ok := <-reader.incoming.frames:
		if !ok {
			return nil, false, reader.incoming.failure
		}
		return item.Clone(), true, nil
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

// Release disconnects a subscriber without withdrawing any pending interaction.
func (hub *Broker) Release(reader *Subscription) {
	if reader == nil {
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for _, group := range []map[*delivery]struct{}{hub.mux, hub.host} {
		if _, ok := group[reader.incoming]; ok {
			delete(group, reader.incoming)
			close(reader.incoming.frames)
			return
		}
	}
}

// Close stops delivery and drops replay state after the owning gateways settle.
func (hub *Broker) Close() {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return
	}
	hub.closed = true
	for _, group := range []map[*delivery]struct{}{hub.mux, hub.host} {
		for source := range group {
			close(source.frames)
			delete(group, source)
		}
	}
	hub.pending = nil
	hub.order = nil
}

// PublishMux encodes a domain event once and broadcasts it to current mux subscribers.
func (hub *Broker) PublishMux(payload MuxFrame) error {
	encoded, err := EncodeMuxFrame(payload)
	if err != nil {
		return err
	}
	return hub.publish(encoded, true)
}

// PublishHostFrame encodes a domain event once and broadcasts it to host subscribers.
func (hub *Broker) PublishHostFrame(payload HostFrame) error {
	encoded, err := EncodeHostFrame(payload)
	if err != nil {
		return err
	}
	return hub.publish(encoded, false)
}
func (hub *Broker) publish(encoded []byte, mux bool) error {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return ErrClosed
	}
	id, err := hub.newRPC()
	if err != nil {
		return err
	}
	recipients := hub.host
	if mux {
		recipients = hub.mux
	}
	return hub.broadcast(recipients, Batch{connection.RPCRequest{
		RPCID:   id,
		Payload: encoded,
	}})
}
func (hub *Broker) broadcast(recipients map[*delivery]struct{}, item Batch) error {
	var failure error
	for source := range recipients {
		select {
		case source.frames <- item:
		default:
			source.failure = ErrSlowConsumer
			close(source.frames)
			delete(recipients, source)
			failure = ErrSlowConsumer
		}
	}
	return failure
}
func cloneRequest(input connection.RPCRequest) connection.RPCRequest {
	input.Payload = slices.Clone(input.Payload)
	return input
}

// PublishPending retains one answerable request until its owning Gateway resolves it.
func (hub *Broker) PublishPending(id connection.RPCID, payload MuxFrame) error {
	encoded, err := EncodeMuxFrame(payload)
	if err != nil {
		return err
	}
	kind := payload.Type()
	if id == "" || (kind != "question/requested" && kind != "approval/requested") {
		return errors.New("apiproxy: invalid pending interaction")
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return ErrClosed
	}
	if _, found := hub.pending[id]; found {
		return fmt.Errorf("apiproxy: rpcId %q already owns a pending interaction", id)
	}
	item := connection.RPCRequest{
		RPCID:   id,
		Payload: encoded,
	}
	hub.pending[id] = publication{
		kind:    kind,
		request: item,
	}
	hub.order = append(hub.order, id)
	// Slow consumers are disconnected; their delivery failure must not withdraw
	// the replayable business request for the remaining and future connections.
	hub.broadcast(hub.mux, Batch{item})
	return nil
}

// ResolvePending removes replay state and broadcasts one terminal frame.
func (hub *Broker) ResolvePending(id connection.RPCID, payload MuxFrame) error {
	encoded, err := EncodeMuxFrame(payload)
	if err != nil {
		return err
	}
	kind := payload.Type()
	if kind != "question/resolved" && kind != "approval/resolved" {
		return errors.New("apiproxy: invalid interaction resolution")
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if _, found := hub.pending[id]; !found {
		return nil
	}
	delete(hub.pending, id)
	hub.order = slices.DeleteFunc(hub.order, func(candidate connection.RPCID) bool { return candidate == id })
	if hub.closed || len(hub.mux) == 0 {
		return nil
	}
	correlation, err := hub.newRPC()
	if err != nil {
		return err
	}
	return hub.broadcast(hub.mux, Batch{connection.RPCRequest{
		RPCID:   correlation,
		Payload: encoded,
	}})
}

// Batch keeps a domain's related frames contiguous in each downlink.
type Batch []connection.RPCRequest

// Clone detaches payload bytes before transferring a batch to a reader.
func (source Batch) Clone() Batch {
	result := make(Batch, len(source))
	for i, item := range source {
		result[i] = cloneRequest(item)
	}
	return result
}

// PublishMuxBatch encodes related frames before publishing any of them.
func (hub *Broker) PublishMuxBatch(payloads []MuxFrame) error {
	batch := make(Batch, len(payloads))
	for i, payload := range payloads {
		encoded, err := EncodeMuxFrame(payload)
		if err != nil {
			return err
		}
		batch[i].Payload = encoded
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return ErrClosed
	}
	for i := range batch {
		id, err := hub.newRPC()
		if err != nil {
			return err
		}
		batch[i].RPCID = id
	}
	return hub.broadcast(hub.mux, batch)
}
