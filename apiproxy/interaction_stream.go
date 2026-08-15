package apiproxy

import (
	"errors"
	"fmt"
	"slices"

	"github.com/gorenx/goren/connection"
)

// InteractionFrameBroker owns the mux lifecycle of answerable interaction
// frames. A requested frame remains replayable with the same rpcId until its
// owner resolves or withdraws it.
type InteractionFrameBroker interface {
	PublishPending(connection.RPCID, MuxFrame) error
	ResolvePending(connection.RPCID, MuxFrame) error
}

func (hub *liveFrameHub) PublishPending(correlationID connection.RPCID, payload MuxFrame) error {
	if payload == nil {
		return errors.New("apiproxy: pending interaction frame is nil")
	}
	if payload.frameType() != "approval/requested" && payload.frameType() != "question/requested" {
		return fmt.Errorf("apiproxy: frame %q is not an answerable interaction", payload.frameType())
	}
	if err := payload.validate(); err != nil {
		return fmt.Errorf("apiproxy: invalid pending interaction frame: %w", err)
	}

	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return errors.New("apiproxy: interaction frame broker is closed")
	}
	if _, exists := hub.pendingInteraction[correlationID]; exists {
		return fmt.Errorf("apiproxy: rpcId %q already owns a pending interaction frame", correlationID)
	}
	pending := pendingInteractionFrame{rpcID: correlationID, payload: payload}
	hub.pendingInteraction[correlationID] = pending
	hub.pendingOrder = append(hub.pendingOrder, correlationID)
	for subscriber := range hub.mux {
		hub.pushMuxRequestLocked(subscriber, pending.rpcID, pending.payload)
	}
	return nil
}

func (hub *liveFrameHub) ResolvePending(correlationID connection.RPCID, payload MuxFrame) error {
	if payload == nil {
		return errors.New("apiproxy: resolved interaction frame is nil")
	}
	if payload.frameType() != "approval/resolved" && payload.frameType() != "question/resolved" {
		return fmt.Errorf("apiproxy: frame %q is not an interaction resolution", payload.frameType())
	}
	if err := payload.validate(); err != nil {
		return fmt.Errorf("apiproxy: invalid resolved interaction frame: %w", err)
	}

	hub.mu.Lock()
	defer hub.mu.Unlock()
	if _, exists := hub.pendingInteraction[correlationID]; !exists {
		return nil
	}
	delete(hub.pendingInteraction, correlationID)
	hub.pendingOrder = slices.DeleteFunc(hub.pendingOrder, func(candidate connection.RPCID) bool {
		return candidate == correlationID
	})
	if hub.closed || len(hub.mux) == 0 {
		return nil
	}
	resolutionID, err := hub.newRPC()
	if err != nil {
		return fmt.Errorf("apiproxy: mint interaction resolution frame: %w", err)
	}
	for subscriber := range hub.mux {
		hub.pushMuxRequestLocked(subscriber, resolutionID, payload)
	}
	return nil
}

func (hub *liveFrameHub) pushMuxRequestLocked(
	subscriber *muxSubscriber,
	correlationID connection.RPCID,
	payload MuxFrame,
) {
	subscriber.queue.push(StreamRequest[MuxFrame]{RPCID: correlationID, Payload: payload})
}
