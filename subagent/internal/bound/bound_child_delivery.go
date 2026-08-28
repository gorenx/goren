package bound

import (
	"context"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/subagent"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

func (child *boundChild) catchUpInteractions() error {
	if !child.deliveryReady {
		return nil
	}
	for {
		advanced, err := child.advanceInteraction()
		if err != nil || !advanced {
			return err
		}
	}
}

func (child *boundChild) advanceInteraction() (bool, error) {
	if err := context.Cause(child.ctx); err != nil {
		return false, nil
	}
	if child.parent == nil || child.parent.SessionValue() == nil ||
		child.agents == nil || !child.agents.Contains(child.parent) {
		return false, nil
	}
	parentSession := child.parent.SessionValue()
	snapshot := parentSession.Snapshot()
	nextSeq, err := boundCursor(
		snapshot.Events,
		child.key.name,
		child.key.childID,
		child.floor,
	)
	if err != nil {
		return false, err
	}
	interaction, found, err := nextParentInteraction(snapshot.Events, nextSeq)
	if err != nil || !found {
		return false, err
	}
	if child.sessions == nil {
		return false, unavailableDependency("Session LiveStore")
	}
	if err = child.sessions.Flush(
		child.ctx,
		parentSession,
	); err != nil {
		return false, err
	}
	current, definitionEnabled, err := child.interactionExecution()
	if err != nil || !definitionEnabled {
		return false, err
	}
	disposition := boundcontract.CursorSkipped
	if interaction.deliverable {
		source := subagent.Delivery{
			ParentSessionID: child.key.parentID,
			Turn:            interaction.turn,
			FromSeq:         interaction.fromSeq,
			ThroughSeq:      interaction.nextSeq - 1,
			Outcome:         interaction.outcome,
		}
		count, receiptErr := countReceipts(
			current.handle.Subject.SessionValue(),
			source,
		)
		if receiptErr != nil {
			return false, receiptErr
		}
		if count == 0 {
			messageValue, messageErr := agentmessage.NewUserMessage(
				agentmessage.UserMessageInput{
					Content: interaction.content,
					Source:  source,
				},
			)
			if messageErr != nil {
				return false, messageErr
			}
			if messageErr = current.handle.Subject.Followup(
				messageValue,
			); messageErr != nil {
				return false, messageErr
			}
		}
		if err = child.sessions.Flush(
			child.ctx,
			current.handle.Subject.SessionValue(),
		); err != nil {
			return false, err
		}
		disposition = boundcontract.CursorDelivered
	}
	_, err = parentSession.Commit(
		child.ctx,
		cursorAdvance{
			name:        child.key.name,
			childID:     child.key.childID,
			floor:       child.floor,
			expected:    nextSeq,
			interaction: interaction,
			disposition: disposition,
		},
	)
	if err != nil {
		return false, err
	}
	if err = child.sessions.Flush(
		child.ctx,
		parentSession,
	); err != nil {
		return false, err
	}
	return true, nil
}

func (child *boundChild) interactionExecution() (
	*residentEpoch,
	bool,
	error,
) {
	definitionValue, found := child.definitions.find(child.key.name)
	if !found {
		return nil, false, nil
	}
	if !definitionValue.Enabled {
		return nil, false, nil
	}
	current := child.current
	if current == nil || current.execution.State() != subagent.ExecutionActive ||
		current.definitionRevision != definitionValue.Revision {
		return nil, false, nil
	}
	return current, true, nil
}

func (child *boundChild) reportInteractionFailure(err error) {
	if child.failures == nil || err == nil {
		return
	}
	child.failures.ReportBoundInteractionFailure(
		InteractionFailure{
			ParentID: child.key.parentID,
			ChildID:  child.key.childID,
			Error:    err,
		},
	)
}
