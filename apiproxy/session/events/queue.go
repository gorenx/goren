package sessionevents

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"

	"github.com/gorenx/goren/session"
)

func projectQueue(header session.Header, events []session.Event) ([]QueueItem, error) {
	startSeq := int64(0)
	if header.SeedLength != nil {
		startSeq = *header.SeedLength
	}
	nextTurn := make([]agentmessage.UserMessage, 0)
	nextStep := make([]agentmessage.UserMessage, 0)
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
		updated := make([]agentmessage.UserMessage, 0, len(*target)-removedCount+len(mutation.Inserted))
		updated = append(updated, (*target)[:mutation.Start]...)
		updated = append(updated, mutation.Inserted...)
		updated = append(updated, (*target)[mutation.Start+removedCount:]...)
		*target = updated
	}
	items := make([]QueueItem, 0, len(nextTurn)+len(nextStep))
	for _, messageValue := range nextTurn {
		projected, err := projectQueuedMessage(messageValue)
		if err != nil {
			return nil, err
		}
		items = append(items, QueueItem{
			ID:        MessageID(messageValue.StableID()),
			Placement: QueueQueued,
			Message:   projected,
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
		items = append(items, QueueItem{
			ID:        MessageID(messageValue.StableID()),
			Placement: placement,
			Message:   projected,
		})
	}
	if items == nil {
		items = []QueueItem{}
	}
	return items, nil
}

func projectQueuedMessage(messageValue agentmessage.UserMessage) (Message, error) {
	blocks := messageValue.ContentValue()
	content := make([]json.RawMessage, len(blocks))
	for index, block := range blocks {
		encoded, err := json.Marshal(block)
		if err != nil {
			return Message{}, err
		}
		content[index] = encoded
	}
	origin, err := json.Marshal(messageValue.SourceValue())
	if err != nil {
		return Message{}, err
	}
	return Message{
		ID:      MessageID(messageValue.StableID()),
		Role:    RoleUser,
		Content: content,
		Source:  origin,
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

// MessageID identifies one immutable entry or transient queue occurrence.
type MessageID string

// QueuePlacement is the Agent-resolved transient inbox placement.
type QueuePlacement string

const (
	QueueQueued   QueuePlacement = "queued"
	QueueSteering QueuePlacement = "steering"
	QueueContext  QueuePlacement = "context"
)

// MessageRole is the provider-neutral conversation role.
type MessageRole string

const (
	RoleSystem    MessageRole = "system"
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
)

// Message is the browser-safe message projection carried by a queue
// snapshot. Content blocks and source remain merge-extensible JSON objects.
type Message struct {
	ID      MessageID         `json:"id"`
	Role    MessageRole       `json:"role"`
	Content []json.RawMessage `json:"content"`
	Source  json.RawMessage   `json:"source"`
}

// QueueItem is one occurrence in the authoritative queue snapshot.
type QueueItem struct {
	ID        MessageID      `json:"id"`
	Placement QueuePlacement `json:"placement"`
	Message   Message        `json:"message"`
}

func validateQueueItems(items []QueueItem) error {
	if items == nil {
		return errors.New("queue items must be an array")
	}
	for index, queued := range items {
		if queued.ID == "" {
			return fmt.Errorf("queue item %d id must be non-empty", index)
		}
		if queued.Placement != QueueQueued && queued.Placement != QueueSteering && queued.Placement != QueueContext {
			return fmt.Errorf("queue item %d has invalid placement", index)
		}
		if err := validateMessage(queued.Message); err != nil {
			return fmt.Errorf("queue item %d: %w", index, err)
		}
	}
	return nil
}

func validateMessage(entry Message) error {
	if entry.ID == "" {
		return errors.New("message id must be non-empty")
	}
	if entry.Role != RoleSystem && entry.Role != RoleUser && entry.Role != RoleAssistant {
		return errors.New("message role is invalid")
	}
	if entry.Content == nil {
		return errors.New("message content must be an array")
	}
	for index, block := range entry.Content {
		if err := validateJSONObjectWithString("content block", block, "type"); err != nil {
			return fmt.Errorf("content block %d: %w", index, err)
		}
	}
	return validateJSONObjectWithString("message source", entry.Source, "kind")
}
