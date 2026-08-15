package apiproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

type streamDeliveryQueue[T any] struct {
	mu     sync.Mutex
	items  []T
	wake   chan struct{}
	closed bool
}

func newStreamDeliveryQueue[T any]() *streamDeliveryQueue[T] {
	return &streamDeliveryQueue[T]{wake: make(chan struct{}, 1)}
}

func (pending *streamDeliveryQueue[T]) push(item T) {
	pending.mu.Lock()
	if pending.closed {
		pending.mu.Unlock()
		return
	}
	pending.items = append(pending.items, item)
	pending.mu.Unlock()
	select {
	case pending.wake <- struct{}{}:
	default:
	}
}

func (pending *streamDeliveryQueue[T]) close() {
	pending.mu.Lock()
	pending.closed = true
	pending.mu.Unlock()
	select {
	case pending.wake <- struct{}{}:
	default:
	}
}

func (pending *streamDeliveryQueue[T]) iterate(requestContext context.Context, emit func(T) error) error {
	for {
		pending.mu.Lock()
		if len(pending.items) != 0 {
			item := pending.items[0]
			pending.items[0] = *new(T)
			pending.items = pending.items[1:]
			pending.mu.Unlock()
			if err := emit(item); err != nil {
				return err
			}
			continue
		}
		closed := pending.closed
		pending.mu.Unlock()
		if closed {
			return nil
		}
		select {
		case <-requestContext.Done():
			return nil
		case <-pending.wake:
		}
	}
}

type muxSubscriber struct {
	queue     *streamDeliveryQueue[StreamRequest[MuxFrame]]
	highwater map[session.SessionID]int64
}

type hostSubscriber struct {
	queue *streamDeliveryQueue[StreamRequest[HostFrame]]
}

type sessionFrameHub struct {
	mu     sync.Mutex
	mux    map[*muxSubscriber]struct{}
	host   map[*hostSubscriber]struct{}
	newRPC func() (connection.RPCID, error)
	closed bool
}

func newSessionFrameHub(newRPC func() (connection.RPCID, error)) *sessionFrameHub {
	return &sessionFrameHub{
		mux: make(map[*muxSubscriber]struct{}), host: make(map[*hostSubscriber]struct{}), newRPC: newRPC,
	}
}

func (hub *sessionFrameHub) openMux(
	requestContext context.Context,
	conversations []*session.Session,
	emit func(StreamRequest[MuxFrame]) error,
) error {
	subscriber := &muxSubscriber{
		queue:     newStreamDeliveryQueue[StreamRequest[MuxFrame]](),
		highwater: make(map[session.SessionID]int64, len(conversations)),
	}
	type baseline struct {
		identifier session.SessionID
		events     []session.Event
		header     session.Header
	}
	baselines := make([]baseline, 0, len(conversations))
	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		return nil
	}
	hub.mux[subscriber] = struct{}{}
	for _, conversation := range conversations {
		events := conversation.Events()
		lastSeq := int64(-1)
		if len(events) != 0 {
			lastSeq = events[len(events)-1].Seq
		}
		subscriber.highwater[conversation.ID()] = lastSeq
		baselines = append(baselines, baseline{
			identifier: conversation.ID(), events: events, header: conversation.Header(),
		})
		if err := hub.pushMuxLocked(subscriber, SessionSubscribedFrame{
			SessionID: SessionID(conversation.ID()), LastSeq: lastSeq,
		}); err != nil {
			delete(hub.mux, subscriber)
			hub.mu.Unlock()
			return err
		}
	}
	for _, snapshot := range baselines {
		items, err := projectQueue(snapshot.header, snapshot.events)
		if err != nil {
			delete(hub.mux, subscriber)
			hub.mu.Unlock()
			return err
		}
		if len(items) != 0 {
			if err := hub.pushMuxLocked(subscriber, SessionQueueFrame{
				SessionID: SessionID(snapshot.identifier), Items: items,
			}); err != nil {
				delete(hub.mux, subscriber)
				hub.mu.Unlock()
				return err
			}
		}
	}
	hub.mu.Unlock()
	defer hub.removeMux(subscriber)
	return subscriber.queue.iterate(requestContext, emit)
}

func (hub *sessionFrameHub) openHost(
	requestContext context.Context,
	emit func(StreamRequest[HostFrame]) error,
) error {
	subscriber := &hostSubscriber{queue: newStreamDeliveryQueue[StreamRequest[HostFrame]]()}
	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		return nil
	}
	hub.host[subscriber] = struct{}{}
	hub.mu.Unlock()
	defer hub.removeHost(subscriber)
	return subscriber.queue.iterate(requestContext, emit)
}

func (hub *sessionFrameHub) sessionEvent(
	identifier session.SessionID,
	committed SessionEvent,
	queueChanged bool,
	items []QueuedInboxItem,
) error {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	var dispatchErr error
	for subscriber := range hub.mux {
		lastSeq, subscribed := subscriber.highwater[identifier]
		if !subscribed || committed.Seq <= lastSeq {
			continue
		}
		if queueChanged {
			if err := hub.pushMuxLocked(subscriber, SessionQueueFrame{
				SessionID: SessionID(identifier), Items: items,
			}); err != nil {
				dispatchErr = errors.Join(dispatchErr, fmt.Errorf(
					"apiproxy: mint queue frame for session %q: %w", identifier, err,
				))
			}
		}
		if err := hub.pushMuxLocked(subscriber, SessionEventFrame{
			SessionID: SessionID(identifier), Event: committed,
		}); err != nil {
			dispatchErr = errors.Join(dispatchErr, fmt.Errorf(
				"apiproxy: mint event frame for session %q at seq %d: %w", identifier, committed.Seq, err,
			))
		} else {
			subscriber.highwater[identifier] = committed.Seq
		}
	}
	return dispatchErr
}

func (hub *sessionFrameHub) sessionCreated(conversation *session.Session) error {
	header := conversation.Header()
	events := conversation.Events()
	lastSeq := int64(-1)
	if len(events) != 0 {
		lastSeq = events[len(events)-1].Seq
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	var dispatchErr error
	for subscriber := range hub.mux {
		if err := hub.pushMuxLocked(subscriber, SessionSubscribedFrame{
			SessionID: SessionID(conversation.ID()), LastSeq: lastSeq,
		}); err != nil {
			dispatchErr = errors.Join(dispatchErr, fmt.Errorf(
				"apiproxy: mint subscribed frame for session %q: %w", conversation.ID(), err,
			))
			continue
		}
		subscriber.highwater[conversation.ID()] = lastSeq
	}
	frameValue := HostSessionAddedFrame{
		SessionID: SessionID(conversation.ID()), Blank: sessionIsBlank(events),
		Origin: string(header.Origin), CWD: cloneStringPointer(header.CWD), AgentPreset: cloneStringPointer(header.AgentPreset),
	}
	if header.ParentSession != nil {
		frameValue.ParentSessionID = SessionID(*header.ParentSession)
	}
	for subscriber := range hub.host {
		if err := hub.pushHostLocked(subscriber, frameValue); err != nil {
			dispatchErr = errors.Join(dispatchErr, fmt.Errorf(
				"apiproxy: mint added frame for session %q: %w", conversation.ID(), err,
			))
		}
	}
	return dispatchErr
}

func (hub *sessionFrameHub) sessionDisposed(identifier session.SessionID) error {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	var dispatchErr error
	for subscriber := range hub.mux {
		delete(subscriber.highwater, identifier)
	}
	for subscriber := range hub.host {
		if err := hub.pushHostLocked(subscriber, HostSessionRemovedFrame{SessionID: SessionID(identifier)}); err != nil {
			dispatchErr = errors.Join(dispatchErr, fmt.Errorf(
				"apiproxy: mint removed frame for session %q: %w", identifier, err,
			))
		}
	}
	return dispatchErr
}

func (hub *sessionFrameHub) agentStatus(identifier session.SessionID, running bool) error {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	var dispatchErr error
	for subscriber := range hub.host {
		if err := hub.pushHostLocked(subscriber, HostSessionStatusFrame{
			SessionID: SessionID(identifier), Running: running,
		}); err != nil {
			dispatchErr = errors.Join(dispatchErr, fmt.Errorf(
				"apiproxy: mint status frame for session %q: %w", identifier, err,
			))
		}
	}
	return dispatchErr
}

func (hub *sessionFrameHub) agentError(identifier session.SessionID, cause error) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	var dispatchErr error
	for subscriber := range hub.host {
		if err := hub.pushHostLocked(subscriber, HostAgentErrorFrame{
			SessionID: SessionID(identifier), Message: message,
		}); err != nil {
			dispatchErr = errors.Join(dispatchErr, fmt.Errorf(
				"apiproxy: mint Agent error frame for session %q: %w", identifier, err,
			))
		}
	}
	return dispatchErr
}

func (hub *sessionFrameHub) pushMuxLocked(subscriber *muxSubscriber, payload MuxFrame) error {
	rpcID, err := hub.newRPC()
	if err != nil {
		return err
	}
	subscriber.queue.push(StreamRequest[MuxFrame]{RPCID: rpcID, Payload: payload})
	return nil
}

func (hub *sessionFrameHub) pushHostLocked(subscriber *hostSubscriber, payload HostFrame) error {
	rpcID, err := hub.newRPC()
	if err != nil {
		return err
	}
	subscriber.queue.push(StreamRequest[HostFrame]{RPCID: rpcID, Payload: payload})
	return nil
}

func (hub *sessionFrameHub) removeMux(subscriber *muxSubscriber) {
	hub.mu.Lock()
	delete(hub.mux, subscriber)
	hub.mu.Unlock()
	subscriber.queue.close()
}

func (hub *sessionFrameHub) removeHost(subscriber *hostSubscriber) {
	hub.mu.Lock()
	delete(hub.host, subscriber)
	hub.mu.Unlock()
	subscriber.queue.close()
}

func (hub *sessionFrameHub) close() {
	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		return
	}
	hub.closed = true
	for subscriber := range hub.mux {
		subscriber.queue.close()
	}
	for subscriber := range hub.host {
		subscriber.queue.close()
	}
	hub.mu.Unlock()
}

func projectQueue(header session.Header, events []session.Event) ([]QueuedInboxItem, error) {
	startSeq := int64(0)
	if header.SeedLength != nil {
		startSeq = *header.SeedLength
	}
	nextTurn := make([]llm.UserMessage, 0)
	nextStep := make([]llm.UserMessage, 0)
	for _, committed := range events {
		if committed.Seq < startSeq || committed.Type != "agent/inbox/spliced" {
			continue
		}
		var mutation agent.InboxSplice
		if err := json.Unmarshal(committed.Data, &mutation); err != nil {
			return nil, fmt.Errorf("apiproxy: project queue at seq %d: %w", committed.Seq, err)
		}
		target := &nextTurn
		if mutation.Target == agent.NextStep {
			target = &nextStep
		} else if mutation.Target != agent.NextTurn {
			return nil, fmt.Errorf("apiproxy: project queue at seq %d: unsupported target", committed.Seq)
		}
		removedCount := 0
		if mutation.RemovedCount != nil {
			removedCount = *mutation.RemovedCount
		}
		if mutation.Start < 0 || removedCount < 0 || mutation.Start+removedCount > len(*target) {
			return nil, fmt.Errorf("apiproxy: project queue at seq %d: invalid splice", committed.Seq)
		}
		updated := make([]llm.UserMessage, 0, len(*target)-removedCount+len(mutation.Inserted))
		updated = append(updated, (*target)[:mutation.Start]...)
		updated = append(updated, mutation.Inserted...)
		updated = append(updated, (*target)[mutation.Start+removedCount:]...)
		*target = updated
	}
	items := make([]QueuedInboxItem, 0, len(nextTurn)+len(nextStep))
	for _, messageValue := range nextTurn {
		projected, err := projectQueuedMessage(messageValue)
		if err != nil {
			return nil, err
		}
		items = append(items, QueuedInboxItem{
			ID: MessageID(messageValue.StableID()), Placement: QueueQueued, Message: projected,
		})
	}
	for _, messageValue := range nextStep {
		projected, err := projectQueuedMessage(messageValue)
		if err != nil {
			return nil, err
		}
		placement := QueueContext
		if messageValue.SourceValue().SourceKind() == "user" {
			placement = QueueSteering
		}
		items = append(items, QueuedInboxItem{
			ID: MessageID(messageValue.StableID()), Placement: placement, Message: projected,
		})
	}
	if items == nil {
		items = []QueuedInboxItem{}
	}
	return items, nil
}

func projectQueuedMessage(messageValue llm.UserMessage) (QueuedMessage, error) {
	blocks := messageValue.ContentValue()
	content := make([]json.RawMessage, len(blocks))
	for index, block := range blocks {
		encoded, err := json.Marshal(block)
		if err != nil {
			return QueuedMessage{}, err
		}
		content[index] = encoded
	}
	origin, err := json.Marshal(messageValue.SourceValue())
	if err != nil {
		return QueuedMessage{}, err
	}
	return QueuedMessage{
		ID: MessageID(messageValue.StableID()), Role: RoleUser, Content: content, Source: origin,
	}, nil
}

func sessionIsBlank(events []session.Event) bool {
	for _, committed := range events {
		if committed.Type == session.TurnStartEventName {
			return false
		}
	}
	return true
}
