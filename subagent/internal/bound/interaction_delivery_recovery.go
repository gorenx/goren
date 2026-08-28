package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/subagent"
)

func (delivery *interactionDelivery) catchUp() error {
	for {
		advanced, err := delivery.advanceOne()
		if err != nil || !advanced {
			return err
		}
	}
}

func (delivery *interactionDelivery) advanceOne() (bool, error) {
	if err := context.Cause(delivery.ctx); err != nil {
		return false, nil
	}
	if delivery.parent == nil || delivery.parent.SessionValue() == nil ||
		delivery.owner.agents == nil ||
		!delivery.owner.agents.Contains(delivery.parent) {
		return false, nil
	}
	parentSession := delivery.parent.SessionValue()
	snapshot := parentSession.Snapshot()
	nextSeq, err := boundCursor(
		snapshot.Events,
		delivery.key.childID,
		delivery.floor,
	)
	if err != nil {
		return false, err
	}
	interaction, found, err := nextParentInteraction(snapshot.Events, nextSeq)
	if err != nil || !found {
		return false, err
	}
	if delivery.owner.sessions == nil {
		return false, unavailableDependency("Session LiveStore")
	}
	if err = delivery.owner.sessions.Flush(
		delivery.ctx,
		parentSession,
	); err != nil {
		return false, err
	}
	delivery.slot.mutex.Lock()
	defer delivery.slot.mutex.Unlock()
	current, enabled, err := delivery.currentExecution()
	if err != nil || !enabled {
		return false, err
	}
	disposition := subagent.BoundCursorSkipped
	if interaction.deliverable {
		source := subagent.Delivery{
			ParentSessionID: delivery.key.parentID,
			Turn:            interaction.turn,
			FromSeq:         interaction.fromSeq,
			ThroughSeq:      interaction.nextSeq - 1,
			Outcome:         interaction.outcome,
		}
		count, receiptErr := countReceipts(
			current.terminator.handle.Subject.SessionValue(),
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
			if messageErr = current.terminator.handle.Subject.Followup(
				messageValue,
			); messageErr != nil {
				return false, messageErr
			}
		}
		if err = delivery.owner.sessions.Flush(
			delivery.ctx,
			current.terminator.handle.Subject.SessionValue(),
		); err != nil {
			return false, err
		}
		disposition = subagent.BoundCursorDelivered
	}
	_, err = parentSession.Commit(
		delivery.ctx,
		cursorAdvance{
			childID:     delivery.key.childID,
			floor:       delivery.floor,
			expected:    nextSeq,
			interaction: interaction,
			disposition: disposition,
		},
	)
	if err != nil {
		return false, err
	}
	if err = delivery.owner.sessions.Flush(
		delivery.ctx,
		parentSession,
	); err != nil {
		return false, err
	}
	return true, nil
}

func (delivery *interactionDelivery) currentExecution() (
	*currentExecution,
	bool,
	error,
) {
	view, err := readBoundProjection(
		delivery.owner.projections,
		delivery.parent.SessionValue(),
	)
	if err != nil {
		return nil, false, err
	}
	config, found := view.Config(delivery.key.childID)
	if !found {
		return nil, false, errors.New("subagent: Bound binding has no config")
	}
	if !config.Config.Enabled {
		return nil, false, nil
	}
	current := delivery.slot.loadCurrent()
	if current == nil || current.running.State() != subagent.ExecutionActive ||
		current.revision != config.Revision ||
		!agent.Same(current.terminator.parent, delivery.parent) {
		return nil, false, nil
	}
	return current, true, nil
}
