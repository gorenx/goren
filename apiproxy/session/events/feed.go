package sessionevents

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/apiproxy/stream"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"
)

// Snapshot is a detached committed Session cut, including its exact lifetime.
type Snapshot struct {
	CreationID string
	Header     session.Header
	Events     []session.Event
}

// Snapshots reads committed Session facts; it never returns mutable Session owners.
type Snapshots interface {
	ListSnapshots(context.Context) ([]Snapshot, error)
	ReadSnapshot(context.Context, session.SessionID, string) (Snapshot, error)
}

// Feed projects Session facts and reconnect baselines onto a shared stream Broker.
// The event subscriber serializes Committed calls in Session event order.
type Feed struct {
	snapshots Snapshots
	broker    *stream.Broker
}

// NewFeed binds snapshot reads and the transport broker without taking their lifecycle.
func NewFeed(source Snapshots, transport *stream.Broker) (*Feed, error) {
	if source == nil || transport == nil {
		return nil, errors.New("apiproxy/session: snapshot source and broker are required")
	}
	return &Feed{
		snapshots: source,
		broker:    transport,
	}, nil
}

// Mux subscribes before reading baselines so commits made during reads remain buffered.
func (source *Feed) Mux(ctx context.Context, emit func(connection.RPCRequest) error) error {
	subscription, err := source.broker.OpenMux()
	if err != nil {
		return err
	}
	defer source.broker.Release(subscription)
	snapshots, err := source.snapshots.ListSnapshots(ctx)
	if err != nil {
		return err
	}
	last := make(map[session.SessionID]int64, len(snapshots))
	for _, snapshot := range snapshots {
		cut := int64(-1)
		if len(snapshot.Events) > 0 {
			cut = snapshot.Events[len(snapshot.Events)-1].Seq
		}
		last[snapshot.Header.ID] = cut
		if err := emitMux(emit, SubscribedFrame{
			SessionID: SessionID(snapshot.Header.ID),
			LastSeq:   cut,
		}); err != nil {
			return err
		}
	}
	for _, frame := range subscription.Replay() {
		if err := emit(frame); err != nil {
			return err
		}
	}
	for _, snapshot := range snapshots {
		items, err := projectQueue(snapshot.Header, snapshot.Events)
		if err != nil {
			return err
		}
		if len(items) != 0 {
			if err := emitMux(emit, QueueFrame{
				SessionID: SessionID(snapshot.Header.ID),
				Items:     items,
			}); err != nil {
				return err
			}
		}
	}
	for {
		batch, found, err := subscription.Next(ctx)
		if err != nil || !found {
			return err
		}
		// Session queue and event frames are one transport batch; the final event
		// supplies the cut for the whole batch so a late callback cannot regress UI.
		if len(batch) == 0 {
			continue
		}
		var edge struct {
			Type      string            `json:"type"`
			SessionID session.SessionID `json:"sessionId"`
			LastSeq   int64             `json:"lastSeq"`
			Event     struct {
				Seq int64 `json:"seq"`
			} `json:"event"`
		}
		if err := json.Unmarshal(batch[len(batch)-1].Payload, &edge); err != nil {
			return err
		}
		switch edge.Type {
		case "session/event":
			cut, exists := last[edge.SessionID]
			// A Session may be created after this connection captured its baseline.
			// Its first event establishes a cut; only known prefixes are duplicates.
			if exists && edge.Event.Seq <= cut {
				continue
			}
			last[edge.SessionID] = edge.Event.Seq
		case "session/subscribed":
			if cut, exists := last[edge.SessionID]; exists && cut >= edge.LastSeq {
				continue
			}
			last[edge.SessionID] = edge.LastSeq
		}
		for _, frame := range batch {
			if err := emit(frame); err != nil {
				return err
			}
		}
	}
}

func emitMux(emit func(connection.RPCRequest) error, payload stream.MuxFrame) error {
	encoded, err := stream.EncodeMuxFrame(payload)
	if err != nil {
		return err
	}
	id, err := stream.NewRPCID()
	if err != nil {
		return err
	}
	return emit(connection.RPCRequest{
		RPCID:   id,
		Payload: encoded,
	})
}

// Committed publishes the committed event and any queue projection atomically.
func (source *Feed) Committed(ctx context.Context, identifier session.SessionID, creationID string, event session.Event) error {
	projected, err := ProjectEvent(event)
	if err != nil {
		return err
	}
	frames := make([]stream.MuxFrame, 0, 2)
	if event.Type == "agent/inbox/spliced" {
		snapshot, err := source.snapshots.ReadSnapshot(ctx, identifier, creationID)
		if err != nil {
			return err
		}
		if snapshot.CreationID != creationID || snapshot.Header.ID != identifier {
			return errors.New("apiproxy/session: snapshot lifetime changed")
		}
		end := 0
		for end < len(snapshot.Events) && snapshot.Events[end].Seq <= event.Seq {
			end++
		}
		if end == 0 || snapshot.Events[end-1].Seq != event.Seq {
			return errors.New("apiproxy/session: committed event missing from snapshot")
		}
		items, err := projectQueue(snapshot.Header, snapshot.Events[:end])
		if err != nil {
			return err
		}
		frames = append(frames, QueueFrame{
			SessionID: SessionID(identifier),
			Items:     items,
		})
	}
	frames = append(frames, EventFrame{
		SessionID: SessionID(identifier),
		Event:     projected,
	})
	return source.broker.PublishMuxBatch(frames)
}

// Created announces a ready Session cut without exposing a business object.
func (source *Feed) Created(snapshot Snapshot) error {
	cut := int64(-1)
	if len(snapshot.Events) > 0 {
		cut = snapshot.Events[len(snapshot.Events)-1].Seq
	}
	if err := source.broker.PublishMux(SubscribedFrame{
		SessionID: SessionID(snapshot.Header.ID),
		LastSeq:   cut,
	}); err != nil {
		return err
	}
	snapshot.Header = snapshot.Header.Clone()
	frame := AddedFrame{
		SessionID:   SessionID(snapshot.Header.ID),
		Blank:       sessionIsBlank(snapshot.Events),
		Origin:      string(snapshot.Header.Origin),
		CWD:         snapshot.Header.CWD,
		AgentPreset: snapshot.Header.AgentPreset,
	}
	if snapshot.Header.ParentSession != nil {
		frame.ParentSessionID = SessionID(*snapshot.Header.ParentSession)
	}
	return source.broker.PublishHostFrame(frame)
}

// StatusChanged publishes the execution owner's state, including final cancellation settlement.
func (source *Feed) StatusChanged(notice agent.StatusNotice) error {
	return source.broker.PublishHostFrame(StatusFrame{
		SessionID: SessionID(notice.AgentID),
		Running:   notice.Status == agent.StatusRunning,
	})
}
