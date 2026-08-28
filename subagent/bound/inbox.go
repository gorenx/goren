package bound

import (
	"context"
	"errors"
	"strings"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

// Address identifies one Bound child by its owning user Session and the
// Definition name that is unique inside that Session.
type Address struct {
	SessionID session.SessionID
	Name      string
}

// InputID is one source-owned stable identity. An input source must reuse the
// same ID when it retries the same logical input after recovery.
type InputID string

// Input is one immutable user-role message offered to a Bound child.
type Input struct {
	ID      InputID
	Content []agentmessage.ContentBlock
	Source  agentmessage.MessageSource
}

// Receipt proves that one Input was durably admitted to the Bound child.
type Receipt struct {
	InputID   InputID
	MessageID agentmessage.MessageID
}

// Inbox is the Bound-owned application boundary for durable input admission.
// Input sources own observation, recovery, retry, and their checkpoints.
type Inbox interface {
	plugin.Service
	Deliver(context.Context, Address, Input) (Receipt, error)
}

// SnapshotInput validates and detaches an input-source value.
func SnapshotInput(source Input) (Input, error) {
	identifier := string(source.ID)
	trimmed := strings.TrimSpace(identifier)
	if trimmed == "" || identifier != trimmed {
		return Input{}, errors.New(
			"subagent/bound: Input ID must be non-empty and trimmed",
		)
	}
	if len(source.Content) == 0 {
		return Input{}, errors.New(
			"subagent/bound: Input content is empty",
		)
	}
	if source.Source == nil {
		return Input{}, errors.New(
			"subagent/bound: Input source is missing",
		)
	}
	content, err := agentmessage.CloneContentBlocks(source.Content)
	if err != nil {
		return Input{}, err
	}
	origin, err := source.Source.CloneSource()
	if err != nil {
		return Input{}, err
	}
	return Input{
		ID:      source.ID,
		Content: content,
		Source:  origin,
	}, nil
}
