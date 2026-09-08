package modelrequest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

// deriveInput derives detached model input from exactly the supplied committed prefix.
func (attempt *Attempt) deriveInput(snapshot session.Snapshot) ([]agentmessage.Message, error) {
	if string(snapshot.Barrier.SessionID) != attempt.options.SessionID {
		return nil, errors.New("agentloop: model input Session identity mismatch")
	}
	if snapshot.Barrier.NextSeq < attempt.confirmedNext || snapshot.Barrier.NextSeq != int64(len(snapshot.Events)) {
		return nil, errors.New("agentloop: model baseline is incomplete or has moved backwards")
	}
	for index, entry := range snapshot.Events {
		if entry.Seq != int64(index) {
			return nil, errors.New("agentloop: model baseline sequence is discontinuous")
		}
	}
	history := make([]agentmessage.Message, 0, len(snapshot.Surface.Nodes))
	for _, sequence := range snapshot.Surface.Nodes {
		if sequence < 0 || sequence >= snapshot.Barrier.NextSeq {
			return nil, fmt.Errorf("agentloop: surface sequence %d is outside the committed log", sequence)
		}
		message, err := session.DeriveEventMessage(snapshot.Events[sequence])
		if err != nil {
			return nil, err
		}
		if message != nil {
			history = append(history, message)
		}
	}
	return history, nil
}

func (attempt *Attempt) checkRequest(snapshot session.Snapshot) error {
	history, err := attempt.deriveInput(snapshot)
	if err != nil {
		return err
	}
	if err := attempt.checkStep(snapshot.Events); err != nil {
		return err
	}
	proposed, err := json.Marshal(attempt.options.Messages)
	if err != nil {
		return err
	}
	derived, err := json.Marshal(history)
	if err != nil {
		return err
	}
	if !bytes.Equal(proposed, derived) {
		return errors.New("agentloop: model messages no longer match the committed Surface")
	}
	header, err := session.LatestRequestHeader(snapshot.Events)
	if err != nil {
		return err
	}
	expected := session.CanonicalEpochHeader(session.EpochHeader{
		Config:          attempt.options.CallConfig,
		Tools:           attempt.options.Tools,
		System:          attempt.options.System,
		AdapterDefaults: &attempt.defaults,
	})
	if header == nil || !session.EpochHeaderEqual(*header, expected) {
		return errors.New("agentloop: model parameters do not match the committed request header")
	}
	attempt.confirmedNext = snapshot.Barrier.NextSeq
	return nil
}

// checkStep rejects attempts for an ended or superseded execution position.
func (attempt *Attempt) checkStep(entries []session.Event) error {
	var turnNumber int64
	var position session.StepPosition
	for _, entry := range entries {
		switch entry.Type {
		case session.TurnStartEventName:
			var started session.TurnStart
			if err := json.Unmarshal(entry.Data, &started); err != nil {
				return err
			}
			turnNumber = started.Turn
			position = session.StepPosition{}
		case session.TurnEndEventName:
			turnNumber = 0
			position = session.StepPosition{}
		case session.StepStartEventName:
			if err := json.Unmarshal(entry.Data, &position); err != nil {
				return err
			}
		case session.StepEndEventName:
			position = session.StepPosition{}
		}
	}
	if turnNumber != attempt.position.Turn || position != attempt.position {
		return errors.New("agentloop: model attempt requires its current open Turn and Step")
	}
	return nil
}

// checkResult rejects protocol mistakes without changing the outstanding operation.
func (attempt *Attempt) checkResult(result Result) error {
	if attempt.phase == proposedAttempt || attempt.phase == completedAttempt {
		return errors.New("agentloop: model attempt has no pending operation")
	}
	if failure, ok := result.(Failed); ok {
		if failure.Err == nil {
			return errors.New("agentloop: failed operation requires an error")
		}
		return nil
	}
	valid := false
	switch attempt.phase {
	case readingLog, checkingRequest:
		_, valid = result.(LogRead)
	case committingHeader, committingContext, committingChunk, committingMessage:
		_, valid = result.(EventsCommitted)
	case openingStream:
		_, valid = result.(GenerationStarted)
	case readingStream:
		_, valid = result.(ChunkRead)
	case closingStream:
		_, valid = result.(StreamClosed)
	}
	if !valid {
		return errors.New("agentloop: result does not match the pending model operation")
	}
	return nil
}
