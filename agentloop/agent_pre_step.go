package agentloop

import (
	"errors"

	"github.com/gorenx/goren/agent"
	turnstate "github.com/gorenx/goren/agentloop/internal/turn"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

// Preparation supplies a committed Session snapshot and resolved prompt data.
// Claimed input is owned by the preparation state, never supplied by the caller.
type Preparation struct {
	Snapshot session.Snapshot
	Prompt   systemprompt.PromptAssembly
}

// PreStep holds detached claimed input and its default admission decision.
type PreStep struct {
	Input   agent.PreStepInput
	Default agent.PreStepDecision
}

type preparationState uint8

const (
	inputUnconfirmed preparationState = iota
	hooksPending
	decisionPending
	contextPending
)

// preparation retains only data and legal admission boundaries, never I/O actions.
type preparation struct {
	after    int64
	state    preparationState
	edit     agent.InboxEdit
	claimed  []agentmessage.UserMessage
	messages []agentmessage.UserMessage
}

// PreparePreStep builds the business middleware request without performing I/O.
func (subject *ReactLoopAgent) PreparePreStep() (PreStep, error) {
	if failure := subject.Cancellation(); failure != nil {
		return PreStep{}, failure
	}
	if subject.preparing == nil || subject.preparing.state != hooksPending || subject.step != nil {
		return PreStep{}, errors.New("agentloop: a Step is already preparing or executing")
	}
	position, err := subject.PreparePosition()
	if err != nil {
		return PreStep{}, err
	}
	claimed := make([]agentmessage.UserMessage, len(subject.preparing.claimed))
	entered := make([]agentmessage.UserMessage, len(subject.preparing.claimed))
	for index, entry := range subject.preparing.claimed {
		claimed[index], err = agentmessage.CloneUserMessage(entry)
		if err != nil {
			return PreStep{}, err
		}
		entered[index], err = agentmessage.CloneUserMessage(entry)
		if err != nil {
			return PreStep{}, err
		}
	}
	subject.preparing.state = decisionPending
	return PreStep{
		Input: agent.PreStepInput{
			AgentID:  subject.identifier,
			Turn:     position.Turn,
			Step:     position.Step,
			Messages: claimed,
		},
		Default: agent.PreStepDecision{
			Kind:     agent.PreStepEnter,
			Messages: entered,
		},
	}, nil
}

// AcceptPreStep records the middleware decision before any context is rendered.
// False means admission was rejected and the Turn must settle.
func (subject *ReactLoopAgent) AcceptPreStep(decision agent.PreStepDecision) (bool, error) {
	if failure := subject.Cancellation(); failure != nil {
		return false, failure
	}
	if subject.preparing == nil || subject.preparing.state != decisionPending || subject.step != nil {
		return false, ErrState
	}
	switch decision.Kind {
	case agent.PreStepReject:
		if len(decision.Messages) != 0 {
			return false, errors.New("agentloop: rejected admission cannot contain messages")
		}
		if err := subject.current.EnterSettling(turnstate.TurnResultBlocked); err != nil {
			return false, err
		}
		subject.preparing = nil
		return false, nil
	case agent.PreStepEnter:
		messages := make([]agentmessage.UserMessage, len(decision.Messages))
		for index, entry := range decision.Messages {
			detached, err := agentmessage.CloneUserMessage(entry)
			if err != nil {
				return false, err
			}
			messages[index] = detached
		}
		subject.preparing.messages = messages
		subject.preparing.claimed = nil
		subject.preparing.state = contextPending
		return true, nil
	default:
		return false, errors.New("agentloop: unsupported pre-step decision")
	}
}

// ConfirmInput accepts the exact claim before preparing any model-visible context.
func (subject *ReactLoopAgent) ConfirmInput(write session.WriteResult) error {
	if subject.preparing == nil || subject.preparing.state != inputUnconfirmed {
		return ErrState
	}
	changes, err := subject.ConfirmEdit(subject.preparing.edit, write)
	if err != nil {
		return err
	}
	subject.preparing.claimed = changes.Claimed
	subject.preparing.edit = agent.InboxEdit{}
	subject.preparing.state = hooksPending
	return nil
}

// AbandonPreparation discards unused context after its claim has been confirmed.
// An unconfirmed claim or proposed Step must be reconciled rather than erased.
func (subject *ReactLoopAgent) AbandonPreparation() error {
	if subject.step != nil || subject.preparing == nil || subject.preparing.state == inputUnconfirmed {
		return ErrState
	}
	subject.preparing = nil
	return nil
}
