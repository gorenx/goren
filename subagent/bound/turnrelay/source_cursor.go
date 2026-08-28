package turnrelay

import (
	"context"
	"errors"

	"github.com/gorenx/goren/session"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

type pendingInput struct {
	interaction interaction
	input       boundcontract.Input
}

// sourceCursor owns ordered Session scanning and the one outstanding input
// for a relay worker.
type sourceCursor struct {
	store        session.LiveStore
	conversation session.Context
	binding      binding
	pending      *pendingInput
}

func newSourceCursor(
	store session.LiveStore,
	conversation session.Context,
	bindingValue binding,
) *sourceCursor {
	return &sourceCursor{
		store:        store,
		conversation: conversation,
		binding:      bindingValue,
	}
}

func (current *sourceCursor) next(
	requestContext context.Context,
) (boundcontract.Input, bool, error) {
	if requestContext == nil {
		return boundcontract.Input{}, false, errors.New(
			"subagent/bound/turnrelay: cursor Context is nil",
		)
	}
	if err := context.Cause(requestContext); err != nil {
		return boundcontract.Input{}, false, err
	}
	if current.pending != nil {
		return current.pending.input, true, nil
	}
	for {
		snapshot := current.conversation.Snapshot()
		nextSeq, err := cursorPosition(snapshot.Events, current.binding)
		if err != nil {
			return boundcontract.Input{}, false, err
		}
		interactionValue, found, err := nextInteraction(
			snapshot.Events,
			nextSeq,
		)
		if err != nil || !found {
			return boundcontract.Input{}, false, err
		}
		if !interactionValue.deliverable {
			if err = current.advance(
				requestContext,
				interactionValue,
				false,
			); err != nil {
				return boundcontract.Input{}, false, err
			}
			continue
		}
		inputValue, err := interactionValue.input(
			current.binding.address.SessionID,
		)
		if err != nil {
			return boundcontract.Input{}, false, err
		}
		if err = current.store.Flush(
			requestContext,
			current.conversation,
		); err != nil {
			return boundcontract.Input{}, false, err
		}
		current.pending = &pendingInput{
			interaction: interactionValue,
			input:       inputValue,
		}
		return inputValue, true, nil
	}
}

func (current *sourceCursor) acknowledge(
	requestContext context.Context,
	receiptValue boundcontract.Receipt,
) error {
	pending := current.pending
	if pending == nil || receiptValue.InputID != pending.input.ID ||
		receiptValue.MessageID == "" {
		return errors.New(
			"subagent/bound/turnrelay: Receipt does not match the pending Input",
		)
	}
	if err := current.advance(
		requestContext,
		pending.interaction,
		true,
	); err != nil {
		return err
	}
	current.pending = nil
	return nil
}

func (current *sourceCursor) advance(
	requestContext context.Context,
	interactionValue interaction,
	delivered bool,
) error {
	if _, err := current.conversation.Commit(
		requestContext,
		cursorPlan{
			binding:     current.binding,
			interaction: interactionValue,
			delivered:   delivered,
		},
	); err != nil {
		return err
	}
	return current.store.Flush(requestContext, current.conversation)
}
