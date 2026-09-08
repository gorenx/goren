package agentloop

import (
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop/internal/visiblecontext"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

// Preparation supplies a committed Session snapshot and resolved prompt data.
// Claimed input is owned by the preparation state, never supplied by the caller.
type Preparation struct {
	Snapshot     session.Snapshot
	Prompt       systemprompt.PromptAssembly
	ToolsVersion uint64
}

// PreStep separates middleware input from its default context-enriched decision.
type PreStep struct {
	Input   agent.PreStepInput
	Default agent.PreStepDecision
}

type preparationState uint8

const (
	inputUnconfirmed preparationState = iota
	contextPending
	decisionPending
)

// preparation retains only data and legal admission boundaries, never I/O actions.
type preparation struct {
	after        int64
	state        preparationState
	edit         agent.InboxEdit
	claimed      []agentmessage.UserMessage
	prompt       systemprompt.PromptAssembly
	toolsVersion uint64
}

// PreparePreStep builds the business middleware request without performing I/O.
func (subject *ReactLoopAgent) PreparePreStep(provided Preparation) (PreStep, error) {
	if failure := subject.Cancellation(); failure != nil {
		return PreStep{}, failure
	}
	if subject.preparing == nil || subject.preparing.state != contextPending || subject.step != nil {
		return PreStep{}, errors.New("agentloop: a Step is already preparing or executing")
	}
	position, err := subject.PreparePosition()
	if err != nil {
		return PreStep{}, err
	}
	if provided.ToolsVersion == 0 || provided.ToolsVersion != subject.toolsVersion {
		return PreStep{}, systemprompt.ErrToolsVersion
	}
	if provided.Snapshot.Barrier.SessionID != subject.identifier ||
		provided.Snapshot.Barrier.NextSeq != int64(len(provided.Snapshot.Events)) {
		return PreStep{}, errors.New("agentloop: pre-step requires the current Session snapshot")
	}
	prompt, err := provided.Prompt.Clone()
	if err != nil {
		return PreStep{}, err
	}
	sections, err := systemprompt.RenderContextSections(prompt)
	if err != nil {
		return PreStep{}, err
	}
	visible := visiblecontext.New(provided.Snapshot)
	contextMessage, present, err := visible.Message(systemprompt.JoinContextSections(sections), sections)
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
	if present {
		entered = append(entered, contextMessage)
	}
	subject.preparing.prompt = prompt
	subject.preparing.toolsVersion = provided.ToolsVersion
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
	subject.preparing.state = contextPending
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

// ToolsVersion returns the effective definition version required by this Agent.
func (subject *ReactLoopAgent) ToolsVersion() uint64 { return subject.toolsVersion }
