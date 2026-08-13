package llm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Event is a closed set of normalized LLM stream events.
type Event interface {
	isEvent()
}

// StartEvent indicates that the provider accepted the request and streaming began.
type StartEvent struct{}

func (StartEvent) isEvent() {}

// TextStartEvent opens one visible text content block.
type TextStartEvent struct {
	ContentIndex int
}

func (TextStartEvent) isEvent() {}

// TextDeltaEvent appends visible text to an open content block.
type TextDeltaEvent struct {
	ContentIndex int
	Delta        string
}

func (TextDeltaEvent) isEvent() {}

// TextEndEvent closes one visible text content block.
type TextEndEvent struct {
	ContentIndex int
	Content      string
}

func (TextEndEvent) isEvent() {}

// ThinkingStartEvent opens one provider-supplied reasoning block.
type ThinkingStartEvent struct {
	ContentIndex int
}

func (ThinkingStartEvent) isEvent() {}

// ThinkingDeltaEvent appends text to an open reasoning block.
type ThinkingDeltaEvent struct {
	ContentIndex int
	Delta        string
}

func (ThinkingDeltaEvent) isEvent() {}

// ThinkingEndEvent closes one reasoning block.
type ThinkingEndEvent struct {
	ContentIndex int
	Content      string
}

func (ThinkingEndEvent) isEvent() {}

// ToolCallStartEvent opens one streamed tool call.
type ToolCallStartEvent struct {
	ContentIndex int
	ID           string
	Name         string
}

func (ToolCallStartEvent) isEvent() {}

// ToolCallDeltaEvent appends JSON argument text to an open tool call.
type ToolCallDeltaEvent struct {
	ContentIndex int
	Delta        string
}

func (ToolCallDeltaEvent) isEvent() {}

// ToolCallEndEvent closes one tool call with parsed JSON arguments.
type ToolCallEndEvent struct {
	ContentIndex int
	ToolCall     ToolCall
}

func (ToolCallEndEvent) isEvent() {}

// DoneEvent is the terminal event for a successful stream.
type DoneEvent struct {
	Reason  StopReason
	Message AssistantMessage
}

func (DoneEvent) isEvent() {}

// ErrorEvent is the terminal event for provider failure or cancellation.
type ErrorEvent struct {
	Reason  StopReason
	Message AssistantMessage
}

func (ErrorEvent) isEvent() {}

// EventStream is an unbounded, single-event-consumer stream. Result can be
// awaited independently, so Complete does not need to drain streaming events.
type EventStream struct {
	mu       sync.Mutex
	events   []Event
	next     int
	notify   chan struct{}
	done     chan struct{}
	terminal bool
	result   AssistantMessage
	partial  AssistantMessage
}

// Snapshot returns an isolated copy of the latest partial Assistant state.
// Consumers may call it after any incremental event.
func (responseStream *EventStream) Snapshot() AssistantMessage {
	responseStream.mu.Lock()
	defer responseStream.mu.Unlock()
	return cloneAssistantMessage(responseStream.partial)
}

func newEventStream() *EventStream {
	return &EventStream{
		notify: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
}

// Next waits for and returns the next event. It returns ok=false after the
// terminal event has been consumed.
func (responseStream *EventStream) Next(ctx context.Context) (Event, bool, error) {
	for {
		responseStream.mu.Lock()
		if responseStream.next < len(responseStream.events) {
			nextEvent := responseStream.events[responseStream.next]
			responseStream.next++
			responseStream.mu.Unlock()
			return nextEvent, true, nil
		}
		terminal := responseStream.terminal
		responseStream.mu.Unlock()

		if terminal {
			return nil, false, nil
		}

		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-responseStream.notify:
		}
	}
}

// Result waits for the terminal event. Provider/runtime failure is represented
// in the returned message; error is reserved for waiting-context cancellation.
func (responseStream *EventStream) Result(ctx context.Context) (AssistantMessage, error) {
	select {
	case <-ctx.Done():
		return AssistantMessage{}, ctx.Err()
	case <-responseStream.done:
		responseStream.mu.Lock()
		assistantReply := cloneAssistantMessage(responseStream.result)
		responseStream.mu.Unlock()
		return assistantReply, nil
	}
}

func (responseStream *EventStream) push(streamEvent Event) bool {
	responseStream.mu.Lock()
	defer responseStream.mu.Unlock()
	if responseStream.terminal {
		return false
	}

	switch terminal := streamEvent.(type) {
	case DoneEvent:
		responseStream.terminal = true
		responseStream.result = cloneAssistantMessage(terminal.Message)
		responseStream.partial = cloneAssistantMessage(terminal.Message)
		close(responseStream.done)
	case ErrorEvent:
		responseStream.terminal = true
		responseStream.result = cloneAssistantMessage(terminal.Message)
		responseStream.partial = cloneAssistantMessage(terminal.Message)
		close(responseStream.done)
	}
	responseStream.events = append(responseStream.events, streamEvent)
	select {
	case responseStream.notify <- struct{}{}:
	default:
	}
	return true
}

// StreamEmitter is the adapter-facing write side of an EventStream.
type StreamEmitter interface {
	Emit(Event) bool
	Update(AssistantMessage) bool
	Done(AssistantMessage) bool
	Fail(error) bool
	FailWith(AssistantMessage, error) bool
	Abort(error) bool
	AbortWith(AssistantMessage, error) bool
}

type streamEmitter struct {
	stream *EventStream
	target Model
}

func (emitter *streamEmitter) Emit(streamEvent Event) bool {
	if streamEvent == nil {
		return false
	}
	switch streamEvent.(type) {
	case DoneEvent, ErrorEvent:
		return false
	default:
		return emitter.stream.push(streamEvent)
	}
}

func (emitter *streamEmitter) Update(assistantReply AssistantMessage) bool {
	emitter.stream.mu.Lock()
	defer emitter.stream.mu.Unlock()
	if emitter.stream.terminal {
		return false
	}
	emitter.stream.partial = cloneAssistantMessage(assistantReply)
	return true
}

func (emitter *streamEmitter) Done(assistantReply AssistantMessage) bool {
	if assistantReply.StopReason != StopReasonStop &&
		assistantReply.StopReason != StopReasonLength &&
		assistantReply.StopReason != StopReasonToolUse {
		return emitter.FailWith(
			assistantReply,
			fmt.Errorf("invalid successful stop reason %q", assistantReply.StopReason),
		)
	}
	return emitter.stream.push(DoneEvent{Reason: assistantReply.StopReason, Message: assistantReply})
}

func (emitter *streamEmitter) Fail(err error) bool {
	return emitter.finishError(StopReasonError, emitter.stream.Snapshot(), err)
}

func (emitter *streamEmitter) FailWith(assistantReply AssistantMessage, err error) bool {
	return emitter.finishError(StopReasonError, assistantReply, err)
}

func (emitter *streamEmitter) Abort(err error) bool {
	return emitter.finishError(StopReasonAborted, emitter.stream.Snapshot(), err)
}

func (emitter *streamEmitter) AbortWith(assistantReply AssistantMessage, err error) bool {
	return emitter.finishError(StopReasonAborted, assistantReply, err)
}

func (emitter *streamEmitter) finishError(reason StopReason, assistantReply AssistantMessage, err error) bool {
	if err == nil {
		err = errors.New(string(reason))
	}
	if assistantReply.API == "" {
		assistantReply.API = emitter.target.API
	}
	if assistantReply.Provider == "" {
		assistantReply.Provider = emitter.target.Provider
	}
	if assistantReply.Model == "" {
		assistantReply.Model = emitter.target.ID
	}
	if assistantReply.Timestamp.IsZero() {
		assistantReply.Timestamp = time.Now()
	}
	assistantReply.StopReason = reason
	assistantReply.ErrorMessage = err.Error()
	return emitter.stream.push(ErrorEvent{Reason: reason, Message: assistantReply})
}

// StreamProducer emits adapter events and must finish with Done, Fail, or Abort.
type StreamProducer func(context.Context, StreamEmitter)

// NewEventStream starts an adapter producer and guarantees exactly one terminal
// event, including when the producer panics or returns without terminating.
func NewEventStream(ctx context.Context, targetModel Model, produce StreamProducer) *EventStream {
	responseStream := newEventStream()
	responseStream.partial = AssistantMessage{
		API:       targetModel.API,
		Provider:  targetModel.Provider,
		Model:     targetModel.ID,
		Timestamp: time.Now(),
	}
	emitter := &streamEmitter{stream: responseStream, target: targetModel}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				emitter.Fail(fmt.Errorf("LLM stream panic: %v", recovered))
				return
			}
			responseStream.mu.Lock()
			terminal := responseStream.terminal
			responseStream.mu.Unlock()
			if !terminal {
				emitter.Fail(ErrInvalidStream)
			}
		}()
		produce(ctx, emitter)
	}()
	return responseStream
}

func cloneAssistantMessage(assistantReply AssistantMessage) AssistantMessage {
	cloned := assistantReply
	cloned.Content = make([]AssistantContent, 0, len(assistantReply.Content))
	for _, contentBlock := range assistantReply.Content {
		switch typedContent := contentBlock.(type) {
		case AssistantTextContent:
			typedContent.Metadata = cloneReplayMetadata(typedContent.Metadata)
			cloned.Content = append(cloned.Content, typedContent)
		case ThinkingContent:
			typedContent.Metadata = cloneReplayMetadata(typedContent.Metadata)
			cloned.Content = append(cloned.Content, typedContent)
		case ToolCall:
			typedContent.Arguments = cloneRawMessage(typedContent.Arguments)
			typedContent.Metadata = cloneReplayMetadata(typedContent.Metadata)
			cloned.Content = append(cloned.Content, typedContent)
		}
	}
	return cloned
}
