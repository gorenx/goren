package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

// InboxOutcome classifies removal as cancellation instead of a loop claim.
type InboxOutcome string

const InboxCanceled InboxOutcome = "canceled"

// InboxSplice is the durable normalized mutation owned by Agent Inbox.
type InboxSplice struct {
	Target       InboxTarget       `json:"target"`
	Start        int               `json:"start"`
	RemovedCount *int              `json:"removedCount,omitempty"`
	Inserted     []llm.UserMessage `json:"inserted"`
	Outcome      InboxOutcome      `json:"outcome,omitempty"`
}

// InboxSpliced is the canonical Session event definition.
var InboxSpliced = session.DefineEvent[InboxSplice]("agent/inbox/spliced")

// InboxNotifications publishes committed projection changes.
type InboxNotifications interface {
	Inserted(llm.UserMessage)
	Discarded(llm.UserMessage)
	Claimed(llm.UserMessage, int64)
}

type inboxState struct {
	nextTurn []llm.UserMessage
	nextStep []llm.UserMessage
}

// Inbox is a replay-once, incrementally updated projection of durable splices.
type Inbox struct {
	operationMu   sync.Mutex
	stateMu       sync.RWMutex
	state         inboxState
	conversation  *session.Session
	notifications InboxNotifications
}

// NewInbox reconstructs pending work after the durable inherited seed boundary.
func NewInbox(conversation *session.Session, notifications InboxNotifications) (*Inbox, error) {
	if conversation == nil || notifications == nil {
		return nil, errors.New("agent: Inbox Session and notifications are required")
	}
	pending := &Inbox{conversation: conversation, notifications: notifications}
	start := int64(0)
	if seedLength := conversation.Header().SeedLength; seedLength != nil {
		start = *seedLength
	}
	for _, event := range conversation.Events() {
		if event.Seq < start || event.Type != "agent/inbox/spliced" {
			continue
		}
		var mutation InboxSplice
		if err := json.Unmarshal(event.Data, &mutation); err != nil {
			return nil, fmt.Errorf("agent: invalid persisted Inbox splice at Session seq %d: %w", event.Seq, err)
		}
		if _, err := pending.apply(mutation); err != nil {
			return nil, fmt.Errorf("agent: invalid persisted Inbox splice at Session seq %d: %w", event.Seq, err)
		}
	}
	return pending, nil
}

// NextTurn returns a detached snapshot of prompts awaiting individual turns.
func (pending *Inbox) NextTurn() []llm.UserMessage {
	pending.stateMu.RLock()
	defer pending.stateMu.RUnlock()
	result, _ := cloneUserMessages(pending.state.nextTurn)
	return result
}

// NextStep returns a detached snapshot of input awaiting the next step.
func (pending *Inbox) NextStep() []llm.UserMessage {
	pending.stateMu.RLock()
	defer pending.stateMu.RUnlock()
	result, _ := cloneUserMessages(pending.state.nextStep)
	return result
}

// HasPending reports whether either list contains work.
func (pending *Inbox) HasPending() bool {
	pending.stateMu.RLock()
	defer pending.stateMu.RUnlock()
	return len(pending.state.nextTurn) != 0 || len(pending.state.nextStep) != 0
}

// Clear durably cancels next-step before next-turn work.
func (pending *Inbox) Clear() error {
	pending.operationMu.Lock()
	defer pending.operationMu.Unlock()
	if _, err := pending.mutateLocked(NextStep, 0, pending.length(NextStep), nil, true); err != nil {
		return err
	}
	_, err := pending.mutateLocked(NextTurn, 0, pending.length(NextTurn), nil, true)
	return err
}

// Claim removes a complete step batch without classifying it as canceled.
func (pending *Inbox) Claim(target InboxTarget, turn int64) ([]llm.UserMessage, error) {
	if target != NextTurn && target != NextStep {
		return nil, fmt.Errorf("agent: unsupported Inbox target %q", target)
	}
	pending.operationMu.Lock()
	defer pending.operationMu.Unlock()
	claimed, err := pending.mutateLocked(NextStep, 0, pending.length(NextStep), nil, false)
	if err != nil {
		return nil, err
	}
	if target == NextTurn {
		queued, claimErr := pending.mutateLocked(NextTurn, 0, 1, nil, false)
		if claimErr != nil {
			return nil, claimErr
		}
		claimed = append(claimed, queued...)
	}
	for _, message := range claimed {
		pending.notifications.Claimed(message, turn)
	}
	return claimed, nil
}

func (pending *Inbox) Append(target InboxTarget, message llm.UserMessage) error {
	pending.operationMu.Lock()
	defer pending.operationMu.Unlock()
	_, err := pending.mutateLocked(target, pending.length(target), 0, []llm.UserMessage{message}, true)
	return err
}

func (pending *Inbox) Prepend(target InboxTarget, message llm.UserMessage) error {
	pending.operationMu.Lock()
	defer pending.operationMu.Unlock()
	_, err := pending.mutateLocked(target, 0, 0, []llm.UserMessage{message}, true)
	return err
}

func (pending *Inbox) Replace(messageID llm.MessageID, replacement llm.UserMessage) (bool, error) {
	pending.operationMu.Lock()
	defer pending.operationMu.Unlock()
	target, index, found := pending.locate(messageID)
	if !found {
		return false, nil
	}
	_, err := pending.mutateLocked(target, index, 1, []llm.UserMessage{replacement}, true)
	return err == nil, err
}

func (pending *Inbox) Remove(messageID llm.MessageID) (bool, error) {
	pending.operationMu.Lock()
	defer pending.operationMu.Unlock()
	target, index, found := pending.locate(messageID)
	if !found {
		return false, nil
	}
	_, err := pending.mutateLocked(target, index, 1, nil, true)
	return err == nil, err
}

// Splice applies Go slice coordinates and records the normalized mutation.
func (pending *Inbox) Splice(target InboxTarget, start int, deleteCount int, inserted []llm.UserMessage) ([]llm.UserMessage, error) {
	pending.operationMu.Lock()
	defer pending.operationMu.Unlock()
	return pending.mutateLocked(target, start, deleteCount, inserted, true)
}

func (pending *Inbox) mutateLocked(
	target InboxTarget,
	start int,
	deleteCount int,
	inserted []llm.UserMessage,
	discardRemoved bool,
) ([]llm.UserMessage, error) {
	pending.stateMu.RLock()
	queue, err := pending.targetLocked(target)
	if err != nil {
		pending.stateMu.RUnlock()
		return nil, err
	}
	actualStart := start
	if actualStart < 0 {
		actualStart = max(len(queue)+actualStart, 0)
	} else {
		actualStart = min(actualStart, len(queue))
	}
	actualDelete := min(max(deleteCount, 0), len(queue)-actualStart)
	pending.stateMu.RUnlock()
	if actualDelete == 0 && len(inserted) == 0 {
		return []llm.UserMessage{}, nil
	}
	insertedSnapshot, err := cloneUserMessages(inserted)
	if err != nil {
		return nil, err
	}
	mutation := InboxSplice{Target: target, Start: actualStart, Inserted: insertedSnapshot}
	if actualDelete != 0 {
		mutation.RemovedCount = intPointer(actualDelete)
		if discardRemoved {
			mutation.Outcome = InboxCanceled
		}
	}
	pending.stateMu.RLock()
	err = pending.validateLocked(mutation)
	pending.stateMu.RUnlock()
	if err != nil {
		return nil, err
	}
	committed, err := session.AppendSerialized(pending.conversation, InboxSpliced, mutation)
	if err != nil {
		return nil, err
	}
	var durable InboxSplice
	if err = json.Unmarshal(committed.Data, &durable); err != nil {
		return nil, fmt.Errorf("agent: decode committed Inbox splice: %w", err)
	}
	removed, err := pending.apply(durable)
	if err != nil {
		return nil, fmt.Errorf("agent: committed Inbox splice violated projection: %w", err)
	}
	if discardRemoved {
		for _, message := range removed {
			pending.notifications.Discarded(message)
		}
	}
	for _, message := range durable.Inserted {
		pending.notifications.Inserted(message)
	}
	return removed, nil
}

func (pending *Inbox) apply(mutation InboxSplice) ([]llm.UserMessage, error) {
	pending.stateMu.Lock()
	defer pending.stateMu.Unlock()
	if err := pending.validateLocked(mutation); err != nil {
		return nil, err
	}
	queue, _ := pending.targetLocked(mutation.Target)
	removedCount := 0
	if mutation.RemovedCount != nil {
		removedCount = *mutation.RemovedCount
	}
	removed, err := cloneUserMessages(queue[mutation.Start : mutation.Start+removedCount])
	if err != nil {
		return nil, err
	}
	replacement, err := cloneUserMessages(mutation.Inserted)
	if err != nil {
		return nil, err
	}
	updated := make([]llm.UserMessage, 0, len(queue)-removedCount+len(replacement))
	updated = append(updated, queue[:mutation.Start]...)
	updated = append(updated, replacement...)
	updated = append(updated, queue[mutation.Start+removedCount:]...)
	if mutation.Target == NextTurn {
		pending.state.nextTurn = updated
	} else {
		pending.state.nextStep = updated
	}
	return removed, nil
}

func (pending *Inbox) validateLocked(mutation InboxSplice) error {
	queue, err := pending.targetLocked(mutation.Target)
	if err != nil {
		return err
	}
	removedCount := 0
	if mutation.RemovedCount != nil {
		removedCount = *mutation.RemovedCount
	}
	if mutation.Start < 0 || mutation.Start > len(queue) || removedCount < 0 || mutation.Start+removedCount > len(queue) {
		return errors.New("agent: invalid Inbox splice")
	}
	if mutation.Outcome != "" && mutation.Outcome != InboxCanceled {
		return errors.New("agent: invalid Inbox outcome")
	}
	insertedSnapshot, err := cloneUserMessages(mutation.Inserted)
	if err != nil {
		return err
	}
	candidate := make([]llm.UserMessage, 0, len(queue)-removedCount+len(insertedSnapshot))
	candidate = append(candidate, queue[:mutation.Start]...)
	candidate = append(candidate, insertedSnapshot...)
	candidate = append(candidate, queue[mutation.Start+removedCount:]...)
	seen := make(map[llm.MessageID]struct{}, len(candidate)+len(pending.state.nextTurn)+len(pending.state.nextStep))
	if mutation.Target == NextTurn {
		err = addUniqueMessages(seen, candidate)
		if err == nil {
			err = addUniqueMessages(seen, pending.state.nextStep)
		}
	} else {
		err = addUniqueMessages(seen, pending.state.nextTurn)
		if err == nil {
			err = addUniqueMessages(seen, candidate)
		}
	}
	return err
}

func (pending *Inbox) targetLocked(target InboxTarget) ([]llm.UserMessage, error) {
	switch target {
	case NextTurn:
		return pending.state.nextTurn, nil
	case NextStep:
		return pending.state.nextStep, nil
	default:
		return nil, fmt.Errorf("agent: unsupported Inbox target %q", target)
	}
}

func (pending *Inbox) length(target InboxTarget) int {
	pending.stateMu.RLock()
	defer pending.stateMu.RUnlock()
	queue, _ := pending.targetLocked(target)
	return len(queue)
}

func (pending *Inbox) locate(messageID llm.MessageID) (InboxTarget, int, bool) {
	pending.stateMu.RLock()
	defer pending.stateMu.RUnlock()
	for _, target := range []InboxTarget{NextTurn, NextStep} {
		queue, _ := pending.targetLocked(target)
		for index, message := range queue {
			if message.StableID() == messageID {
				return target, index, true
			}
		}
	}
	return "", 0, false
}

func addUniqueMessages(seen map[llm.MessageID]struct{}, messages []llm.UserMessage) error {
	for _, message := range messages {
		identifier := message.StableID()
		if identifier == "" {
			return errors.New("agent: pending message id is empty")
		}
		if _, exists := seen[identifier]; exists {
			return fmt.Errorf("agent: message %q is already pending", identifier)
		}
		seen[identifier] = struct{}{}
	}
	return nil
}

func cloneUserMessages(messages []llm.UserMessage) ([]llm.UserMessage, error) {
	result := make([]llm.UserMessage, len(messages))
	for index, message := range messages {
		copyValue, err := llm.CloneUserMessage(message)
		if err != nil {
			return nil, fmt.Errorf("agent: clone pending message %d: %w", index, err)
		}
		result[index] = copyValue
	}
	return result, nil
}

func intPointer(value int) *int { return &value }

type inboxSpliceWire struct {
	Target       json.RawMessage `json:"target"`
	Start        json.RawMessage `json:"start"`
	RemovedCount json.RawMessage `json:"removedCount"`
	Inserted     json.RawMessage `json:"inserted"`
	Outcome      json.RawMessage `json:"outcome"`
}

// UnmarshalJSON restores durable specialized messages through the LLM owner.
func (mutation *InboxSplice) UnmarshalJSON(encoded []byte) error {
	if mutation == nil {
		return errors.New("agent: cannot decode Inbox splice into nil target")
	}
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return errors.New("agent: Inbox splice must be an object")
	}
	var wireValue inboxSpliceWire
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wireValue); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("agent: Inbox splice contains multiple JSON values")
		}
		return err
	}
	if len(wireValue.Target) == 0 || bytes.Equal(bytes.TrimSpace(wireValue.Target), []byte("null")) {
		return errors.New("agent: Inbox splice target is required")
	}
	if len(wireValue.Start) == 0 || bytes.Equal(bytes.TrimSpace(wireValue.Start), []byte("null")) {
		return errors.New("agent: Inbox splice start is required")
	}
	if len(wireValue.Inserted) == 0 || bytes.Equal(bytes.TrimSpace(wireValue.Inserted), []byte("null")) {
		return errors.New("agent: Inbox splice inserted must be an array")
	}
	var target InboxTarget
	if err := json.Unmarshal(wireValue.Target, &target); err != nil {
		return errors.New("agent: Inbox splice target must be a string")
	}
	var start int
	if err := json.Unmarshal(wireValue.Start, &start); err != nil {
		return errors.New("agent: Inbox splice start must be an integer")
	}
	var removedCount *int
	if len(wireValue.RemovedCount) != 0 {
		if bytes.Equal(bytes.TrimSpace(wireValue.RemovedCount), []byte("null")) {
			return errors.New("agent: Inbox splice removedCount must be an integer")
		}
		var decodedCount int
		if err := json.Unmarshal(wireValue.RemovedCount, &decodedCount); err != nil {
			return errors.New("agent: Inbox splice removedCount must be an integer")
		}
		removedCount = &decodedCount
	}
	var rawMessages []json.RawMessage
	if err := json.Unmarshal(wireValue.Inserted, &rawMessages); err != nil {
		return errors.New("agent: Inbox splice inserted must be an array")
	}
	inserted := make([]llm.UserMessage, len(rawMessages))
	for index, rawValue := range rawMessages {
		message, err := llm.DecodeUserMessage(rawValue)
		if err != nil {
			return fmt.Errorf("agent: invalid inserted message %d: %w", index, err)
		}
		inserted[index] = message
	}
	var outcome InboxOutcome
	if len(wireValue.Outcome) != 0 {
		if bytes.Equal(bytes.TrimSpace(wireValue.Outcome), []byte("null")) || json.Unmarshal(wireValue.Outcome, &outcome) != nil {
			return errors.New("agent: Inbox splice outcome must be canceled")
		}
	}
	*mutation = InboxSplice{
		Target: target, Start: start,
		RemovedCount: cloneInt(removedCount), Inserted: inserted, Outcome: outcome,
	}
	return nil
}

func cloneInt(source *int) *int {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}
