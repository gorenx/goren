package llmactor

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/gorenx/goren/llm"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Receivers supplies narrow result handlers for the current Actor turn only.
// Client never retains these interfaces or a reference to the owning Actor.
type Receivers struct {
	Generation   GenerationReceiver
	Chunk        ChunkReceiver
	Close        CloseReceiver
	Cancellation CancellationReceiver
	Model        ModelReceiver
}

// GenerationReceiver consumes the actual opening outcome on its Actor turn.
type GenerationReceiver interface {
	OnGenerationStarted(*goakt.GrainContext, GenerationStarted)
}

// ChunkReceiver consumes one requested stream item on its Actor turn.
type ChunkReceiver interface {
	OnChunkRead(*goakt.GrainContext, ChunkRead)
}

// CloseReceiver consumes the actual resource release outcome on its Actor turn.
type CloseReceiver interface {
	OnCallClosed(*goakt.GrainContext, CallClosed)
}

// CancellationReceiver consumes cancellation acknowledgement without implying closure.
type CancellationReceiver interface {
	OnCancellationAccepted(*goakt.GrainContext, CancellationAccepted)
}

// ModelReceiver consumes one exact-model metadata result on its Actor turn.
type ModelReceiver interface {
	OnModelResolved(*goakt.GrainContext, ModelResolved)
}

// Client adapts Grain consumers to the asynchronous LLM Router protocol.
// One owning Grain serializes all methods, including Receive and Dispose.
// It owns no model configuration, business retry policy or provider resource.
type Client struct {
	router  *goakt.PID
	timeout time.Duration
	pending map[RequestID]*request
}

type request struct {
	timer  string
	callID CallID
	accept func(*goakt.GrainContext, any, error, Receivers)
}

type requestExpired struct {
	endpoint *Client
	id       RequestID
}

// NewClient binds an existing Router without creating framework owners.
func NewClient(router *goakt.PID, timeout time.Duration) (*Client, error) {
	if router == nil || timeout <= 0 {
		return nil, errors.New("llm: Router and positive request timeout are required")
	}
	return &Client{
		router:  router,
		timeout: timeout,
		pending: make(map[RequestID]*request),
	}, nil
}

// NewCall identifies a generation independently of its individual requests.
// Retry of a read or close keeps this identity; another generation uses a new one.
func (*Client) NewCall() CallID { return CallID(uuid.NewString()) }

// Start opens a call once. An opening error still requires Close for that identity.
func (endpoint *Client) Start(gc *goakt.GrainContext, id CallID, settings Settings, input llm.Input, receiver GenerationReceiver) {
	route := endpoint.route(gc, id)
	endpoint.send(gc, route.RequestID, id, Receivers{
		Generation: receiver,
	}, Generation{
		Delivery: route,
		Settings: settings,
		Input:    input,
	}, func(current *goakt.GrainContext, payload any, failure error, handlers Receivers) {
		result, ok := payload.(GenerationStarted)
		if !ok && failure == nil {
			failure = errors.New("llm: invalid generation reply")
		}
		if failure != nil {
			result = GenerationStarted{
				CallID:    id,
				RequestID: route.RequestID,
				Err:       failure,
			}
		}
		handlers.Generation.OnGenerationStarted(current, result)
	})
}

// Read requests one chunk; the consumer decides when another item is needed.
func (endpoint *Client) Read(gc *goakt.GrainContext, id CallID, receiver ChunkReceiver) {
	route := endpoint.route(gc, id)
	endpoint.send(gc, route.RequestID, id, Receivers{
		Chunk: receiver,
	}, ReadChunk{
		Delivery: route,
	}, func(current *goakt.GrainContext, payload any, failure error, handlers Receivers) {
		result, ok := payload.(ChunkRead)
		if !ok && failure == nil {
			failure = errors.New("llm: invalid chunk reply")
		}
		if failure != nil {
			result = ChunkRead{
				CallID:    id,
				RequestID: route.RequestID,
				Err:       failure,
			}
		}
		handlers.Chunk.OnChunkRead(current, result)
	})
}

// Close requests release of the same call, including after an uncertain opening.
func (endpoint *Client) Close(gc *goakt.GrainContext, id CallID, receiver CloseReceiver) {
	route := endpoint.route(gc, id)
	endpoint.send(gc, route.RequestID, id, Receivers{
		Close: receiver,
	}, CloseCall{
		Delivery: route,
	}, func(current *goakt.GrainContext, payload any, failure error, handlers Receivers) {
		result, ok := payload.(CallClosed)
		if !ok && failure == nil {
			failure = errors.New("llm: invalid close reply")
		}
		if failure != nil {
			result = CallClosed{
				CallID:    id,
				RequestID: route.RequestID,
				Err:       failure,
			}
		}
		handlers.Close.OnCallClosed(current, result)
	})
}

// Cancel interrupts call I/O; callers must still request Close afterwards.
func (endpoint *Client) Cancel(gc *goakt.GrainContext, id CallID, cause error, receiver CancellationReceiver) {
	route := endpoint.route(gc, id)
	endpoint.send(gc, route.RequestID, id, Receivers{
		Cancellation: receiver,
	}, CancelCall{
		Delivery: route,
		Cause:    cause,
	}, func(current *goakt.GrainContext, payload any, failure error, handlers Receivers) {
		result, ok := payload.(CancellationAccepted)
		if !ok && failure == nil {
			failure = errors.New("llm: invalid cancellation reply")
		}
		if failure != nil {
			result = CancellationAccepted{
				CallID:    id,
				RequestID: route.RequestID,
				Err:       failure,
			}
		}
		handlers.Cancellation.OnCancellationAccepted(current, result)
	})
}

// ResolveModel requests metadata without opening a generation.
func (endpoint *Client) ResolveModel(gc *goakt.GrainContext, provider string, modelID string, receiver ModelReceiver) {
	id := RequestID(uuid.NewString())
	endpoint.send(gc, id, "", Receivers{
		Model: receiver,
	}, ResolveModel{
		Query: Query{
			RequestID: id,
			ReplyTo:   gc.Self(),
			Deadline:  time.Now().Add(endpoint.timeout),
		},
		Provider: provider,
		Model:    modelID,
	}, func(current *goakt.GrainContext, payload any, failure error, handlers Receivers) {
		result, ok := payload.(ModelResolved)
		if !ok && failure == nil {
			failure = errors.New("llm: invalid model reply")
		}
		if failure != nil {
			result = ModelResolved{
				RequestID: id,
				Err:       failure,
			}
		}
		handlers.Model.OnModelResolved(current, result)
	})
}

func (endpoint *Client) route(gc *goakt.GrainContext, id CallID) Delivery {
	return Delivery{
		CallID:    id,
		RequestID: RequestID(uuid.NewString()),
		ReplyTo:   gc.Self(),
	}
}

func (endpoint *Client) send(gc *goakt.GrainContext, id RequestID, callID CallID, handlers Receivers, message any, accept func(*goakt.GrainContext, any, error, Receivers)) {
	pending := &request{
		callID: callID,
		accept: accept,
	}
	endpoint.pending[id] = pending
	reference, err := gc.ScheduleOnce(requestExpired{
		endpoint: endpoint,
		id:       id,
	}, endpoint.timeout)
	if err == nil {
		pending.timer = reference
		err = goakt.Tell(gc.Context(), endpoint.router, goakt.NewBroadcast(message))
	}
	if err != nil {
		endpoint.complete(gc, id, nil, err, handlers)
	}
}

// Receive consumes recognized LLM replies and this client's deadlines.
// Completion uses the current GrainContext; no context is retained across turns.
// Unknown and late request IDs are acknowledged without invoking a receiver.
func (endpoint *Client) Receive(gc *goakt.GrainContext, handlers Receivers) bool {
	var id RequestID
	var callID CallID
	switch input := gc.Message().(type) {
	case requestExpired:
		if input.endpoint != endpoint {
			return false
		}
		gc.NoErr()
		endpoint.complete(gc, input.id, nil, context.DeadlineExceeded, handlers)
		return true
	case GenerationStarted:
		id, callID = input.RequestID, input.CallID
	case ChunkRead:
		id, callID = input.RequestID, input.CallID
	case CallClosed:
		id, callID = input.RequestID, input.CallID
	case CancellationAccepted:
		id, callID = input.RequestID, input.CallID
	case ModelResolved:
		id = input.RequestID
	default:
		return false
	}
	gc.NoErr()
	pending := endpoint.pending[id]
	if pending == nil || pending.callID != callID {
		return true
	}
	endpoint.complete(gc, id, gc.Message(), nil, handlers)
	return true
}

func (endpoint *Client) complete(gc *goakt.GrainContext, id RequestID, payload any, failure error, handlers Receivers) {
	pending := endpoint.pending[id]
	if pending == nil {
		return
	}
	delete(endpoint.pending, id)
	if pending.timer != "" {
		_ = gc.CancelSchedule(pending.timer)
	}
	if failure != nil && pending.callID == "" {
		_ = goakt.Tell(gc.Context(), endpoint.router, goakt.NewBroadcast(CancelQuery{
			RequestID: id,
			Cause:     failure,
		}))
	}
	pending.accept(gc, payload, failure, handlers)
}

// Dispose cancels local delivery tracking after the owner has settled its calls.
// It does not claim that remote provider resources have been released.
func (endpoint *Client) Dispose(gc *goakt.GrainContext) {
	for id, pending := range endpoint.pending {
		if pending.timer != "" {
			_ = gc.CancelSchedule(pending.timer)
		}
		delete(endpoint.pending, id)
	}
}
