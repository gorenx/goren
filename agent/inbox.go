package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

// InboxOutcome classifies removal as cancellation instead of a loop claim.
type InboxOutcome string

const InboxCanceled InboxOutcome = "canceled"

// InboxSplice is the durable normalized mutation owned by Agent Inbox.
type InboxSplice struct {
	Target       InboxTarget                `json:"target"`
	Start        int                        `json:"start"`
	RemovedCount *int                       `json:"removedCount,omitempty"`
	Inserted     []agentmessage.UserMessage `json:"inserted"`
	Outcome      InboxOutcome               `json:"outcome,omitempty"`
}

const InboxSplicedEventName = "agent/inbox/spliced"

// InboxSpliced is the canonical Session event definition.
var InboxSpliced = session.DefineEvent[InboxSplice](InboxSplicedEventName)

// InboxChanges describes business effects after committed splices are applied.
type InboxChanges struct {
	Inserted  []agentmessage.UserMessage
	Discarded []agentmessage.UserMessage
	Claimed   []agentmessage.UserMessage
}

// InboxEdit contains one atomic Session batch. Removed messages are not claimed
// or cancelled until the owner receives a successful Session commit.
type InboxEdit struct {
	Drafts  []session.EventDraft
	Removed []agentmessage.UserMessage
}

type inboxState struct {
	nextTurn []agentmessage.UserMessage
	nextStep []agentmessage.UserMessage
}

// Inbox is the single-owner projection of durable pending-message splices.
type Inbox struct {
	state inboxState
}

// NewInbox rebuilds pending work after the inherited seed boundary.
func NewInbox(metadata session.Header, entries []session.Event) (*Inbox, error) {
	start := int64(0)
	if metadata.SeedLength != nil {
		start = *metadata.SeedLength
	}
	if start < 0 || start > int64(len(entries)) {
		return nil, errors.New("agent: invalid Inbox seed boundary")
	}
	pending := &Inbox{}
	if _, err := pending.Apply(entries[start:]); err != nil {
		return nil, err
	}
	return pending, nil
}

// NextTurn returns detached prompts awaiting individual turns.
func (pending *Inbox) NextTurn() []agentmessage.UserMessage {
	result, _ := cloneUserMessages(pending.state.nextTurn)
	return result
}

// NextStep returns detached messages awaiting the next step.
func (pending *Inbox) NextStep() []agentmessage.UserMessage {
	result, _ := cloneUserMessages(pending.state.nextStep)
	return result
}

// HasPending reports whether either business list contains work.
func (pending *Inbox) HasPending() bool {
	return len(pending.state.nextTurn) != 0 || len(pending.state.nextStep) != 0
}

// Apply atomically folds an ordered, not-yet-applied batch of committed events.
func (pending *Inbox) Apply(entries []session.Event) (InboxChanges, error) {
	candidate := &Inbox{
		state: inboxState{
			nextTurn: pending.NextTurn(),
			nextStep: pending.NextStep(),
		},
	}
	changes := InboxChanges{}
	for _, entry := range entries {
		if entry.Type != InboxSplicedEventName {
			continue
		}
		var mutation InboxSplice
		if err := json.Unmarshal(entry.Data, &mutation); err != nil {
			return InboxChanges{}, fmt.Errorf("agent: invalid Inbox splice at seq %d: %w", entry.Seq, err)
		}
		removed, err := candidate.apply(mutation)
		if err != nil {
			return InboxChanges{}, fmt.Errorf("agent: invalid Inbox splice at seq %d: %w", entry.Seq, err)
		}
		inserted, err := cloneUserMessages(mutation.Inserted)
		if err != nil {
			return InboxChanges{}, err
		}
		changes.Inserted = append(changes.Inserted, inserted...)
		if mutation.Outcome == InboxCanceled {
			changes.Discarded = append(changes.Discarded, removed...)
		} else {
			changes.Claimed = append(changes.Claimed, removed...)
		}
	}
	pending.state = candidate.state
	return changes, nil
}

// PrepareSplice normalizes slice coordinates without changing online state.
func (pending *Inbox) PrepareSplice(target InboxTarget, start int, deleteCount int, inserted []agentmessage.UserMessage) (InboxEdit, error) {
	return pending.prepare(target, start, deleteCount, inserted, true)
}

func (pending *Inbox) prepare(target InboxTarget, start int, deleteCount int, inserted []agentmessage.UserMessage, discard bool) (InboxEdit, error) {
	messages, err := pending.target(target)
	if err != nil {
		return InboxEdit{}, err
	}
	if start < 0 {
		start = max(len(messages)+start, 0)
	} else {
		start = min(start, len(messages))
	}
	count := min(max(deleteCount, 0), len(messages)-start)
	if count == 0 && len(inserted) == 0 {
		return InboxEdit{}, nil
	}
	detached, err := cloneUserMessages(inserted)
	if err != nil {
		return InboxEdit{}, err
	}
	mutation := InboxSplice{
		Target:   target,
		Start:    start,
		Inserted: detached,
	}
	if count != 0 {
		mutation.RemovedCount = intPointer(count)
		if discard {
			mutation.Outcome = InboxCanceled
		}
	}
	if err := pending.validate(mutation); err != nil {
		return InboxEdit{}, err
	}
	draft, err := session.NewEventDraft(InboxSpliced, mutation)
	if err != nil {
		return InboxEdit{}, err
	}
	removed, err := cloneUserMessages(messages[start : start+count])
	if err != nil {
		return InboxEdit{}, err
	}
	return InboxEdit{
		Drafts:  []session.EventDraft{draft},
		Removed: removed,
	}, nil
}

// PrepareAppend stages a message after existing work in the selected list.
func (pending *Inbox) PrepareAppend(target InboxTarget, message agentmessage.UserMessage) (InboxEdit, error) {
	return pending.PrepareSplice(target, pending.length(target), 0, []agentmessage.UserMessage{message})
}

// PreparePrepend stages a message before existing work in the selected list.
func (pending *Inbox) PreparePrepend(target InboxTarget, message agentmessage.UserMessage) (InboxEdit, error) {
	return pending.PrepareSplice(target, 0, 0, []agentmessage.UserMessage{message})
}

// PrepareReplace returns an empty edit when the message is no longer pending.
func (pending *Inbox) PrepareReplace(id agentmessage.MessageID, replacement agentmessage.UserMessage) (InboxEdit, error) {
	target, index, found := pending.locate(id)
	if !found {
		return InboxEdit{}, nil
	}
	return pending.PrepareSplice(target, index, 1, []agentmessage.UserMessage{replacement})
}

// PrepareRemove stages cancellation of an existing message, or an empty edit.
func (pending *Inbox) PrepareRemove(id agentmessage.MessageID) (InboxEdit, error) {
	target, index, found := pending.locate(id)
	if !found {
		return InboxEdit{}, nil
	}
	return pending.PrepareSplice(target, index, 1, nil)
}

// PrepareClaim stages all next-step messages followed by at most one next-turn message.
func (pending *Inbox) PrepareClaim(target InboxTarget) (InboxEdit, error) {
	if target != NextTurn && target != NextStep {
		return InboxEdit{}, fmt.Errorf("agent: unsupported Inbox target %q", target)
	}
	first, err := pending.prepare(NextStep, 0, pending.length(NextStep), nil, false)
	if err != nil || target == NextStep {
		return first, err
	}
	second, err := pending.prepare(NextTurn, 0, 1, nil, false)
	if err != nil {
		return InboxEdit{}, err
	}
	first.Drafts = append(first.Drafts, second.Drafts...)
	first.Removed = append(first.Removed, second.Removed...)
	return first, nil
}

// PrepareClear stages next-step cancellation before next-turn in one Session batch.
func (pending *Inbox) PrepareClear() (InboxEdit, error) {
	first, err := pending.prepare(NextStep, 0, pending.length(NextStep), nil, true)
	if err != nil {
		return InboxEdit{}, err
	}
	second, err := pending.prepare(NextTurn, 0, pending.length(NextTurn), nil, true)
	if err != nil {
		return InboxEdit{}, err
	}
	first.Drafts = append(first.Drafts, second.Drafts...)
	first.Removed = append(first.Removed, second.Removed...)
	return first, nil
}

func (pending *Inbox) apply(mutation InboxSplice) ([]agentmessage.UserMessage, error) {

	if err := pending.validate(mutation); err != nil {
		return nil, err
	}
	queue, _ := pending.target(mutation.Target)
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
	updated := make([]agentmessage.UserMessage, 0, len(queue)-removedCount+len(replacement))
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

func (pending *Inbox) validate(mutation InboxSplice) error {
	queue, err := pending.target(mutation.Target)
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
	candidate := make([]agentmessage.UserMessage, 0, len(queue)-removedCount+len(insertedSnapshot))
	candidate = append(candidate, queue[:mutation.Start]...)
	candidate = append(candidate, insertedSnapshot...)
	candidate = append(candidate, queue[mutation.Start+removedCount:]...)
	seen := make(map[agentmessage.MessageID]struct{}, len(candidate)+len(pending.state.nextTurn)+len(pending.state.nextStep))
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

func (pending *Inbox) target(target InboxTarget) ([]agentmessage.UserMessage, error) {
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

	queue, _ := pending.target(target)
	return len(queue)
}

func (pending *Inbox) locate(messageID agentmessage.MessageID) (InboxTarget, int, bool) {

	for _, target := range []InboxTarget{NextTurn, NextStep} {
		queue, _ := pending.target(target)
		for index, message := range queue {
			if message.StableID() == messageID {
				return target, index, true
			}
		}
	}
	return "", 0, false
}

func addUniqueMessages(seen map[agentmessage.MessageID]struct{}, messages []agentmessage.UserMessage) error {
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

func cloneUserMessages(messages []agentmessage.UserMessage) ([]agentmessage.UserMessage, error) {
	result := make([]agentmessage.UserMessage, len(messages))
	for index, message := range messages {
		copyValue, err := agentmessage.CloneUserMessage(message)
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
	inserted := make([]agentmessage.UserMessage, len(rawMessages))
	for index, rawValue := range rawMessages {
		message, err := agentmessage.DecodeUserMessage(rawValue)
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
		Target:       target,
		Start:        start,
		RemovedCount: cloneInt(removedCount),
		Inserted:     inserted,
		Outcome:      outcome,
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
