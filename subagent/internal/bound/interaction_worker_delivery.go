package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/subagent"
)

func (worker *interactionWorker) catchUp() error {
	for {
		advanced, err := worker.advanceOne()
		if err != nil || !advanced {
			return err
		}
	}
}

func (worker *interactionWorker) advanceOne() (bool, error) {
	if err := context.Cause(worker.ctx); err != nil {
		return false, nil
	}
	if worker.parent == nil || worker.parent.SessionValue() == nil ||
		worker.owner.dependencies.Agents == nil ||
		!worker.owner.dependencies.Agents.Contains(worker.parent) {
		return false, nil
	}
	parentSession := worker.parent.SessionValue()
	snapshot := parentSession.Snapshot()
	nextSeq, err := boundCursor(
		snapshot.Events,
		worker.key.childID,
		worker.floor,
	)
	if err != nil {
		return false, err
	}
	interaction, found, err := nextParentInteraction(snapshot.Events, nextSeq)
	if err != nil || !found {
		return false, err
	}
	if worker.owner.dependencies.Sessions == nil {
		return false, unavailableDependency("Session LiveStore")
	}
	if err = worker.owner.dependencies.Sessions.Flush(
		worker.ctx,
		parentSession,
	); err != nil {
		return false, err
	}
	worker.operation.mutex.Lock()
	defer worker.operation.mutex.Unlock()
	current, enabled, err := worker.currentExecution()
	if err != nil || !enabled {
		return false, err
	}
	disposition := subagent.BoundCursorSkipped
	if interaction.deliverable {
		delivery := subagent.Delivery{
			ParentSessionID: worker.key.parentID,
			Turn:            interaction.turn,
			FromSeq:         interaction.fromSeq,
			ThroughSeq:      interaction.nextSeq - 1,
			Outcome:         interaction.outcome,
		}
		count, receiptErr := countReceipts(
			current.terminator.handle.Subject.SessionValue(),
			delivery,
		)
		if receiptErr != nil {
			return false, receiptErr
		}
		if count == 0 {
			messageValue, messageErr := agentmessage.NewUserMessage(
				agentmessage.UserMessageInput{
					Content: interaction.content,
					Source:  delivery,
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
		if err = worker.owner.dependencies.Sessions.Flush(
			worker.ctx,
			current.terminator.handle.Subject.SessionValue(),
		); err != nil {
			return false, err
		}
		disposition = subagent.BoundCursorDelivered
	}
	_, err = parentSession.Commit(
		worker.ctx,
		cursorAdvance{
			childID:     worker.key.childID,
			floor:       worker.floor,
			expected:    nextSeq,
			interaction: interaction,
			disposition: disposition,
		},
	)
	if err != nil {
		return false, err
	}
	if err = worker.owner.dependencies.Sessions.Flush(
		worker.ctx,
		parentSession,
	); err != nil {
		return false, err
	}
	return true, nil
}

func (worker *interactionWorker) currentExecution() (
	*currentExecution,
	bool,
	error,
) {
	view, err := worker.owner.parentView(worker.parent.SessionValue())
	if err != nil {
		return nil, false, err
	}
	config, found := view.Config(worker.key.childID)
	if !found {
		return nil, false, errors.New("subagent: Bound binding has no config")
	}
	if !config.Config.Enabled {
		return nil, false, nil
	}
	current := worker.operation.loadCurrent()
	if current == nil || current.running.State() != subagent.ExecutionActive ||
		current.revision != config.Revision ||
		!agent.Same(current.terminator.parent, worker.parent) {
		return nil, false, nil
	}
	return current, true, nil
}
