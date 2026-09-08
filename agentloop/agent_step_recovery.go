package agentloop

import (
	"encoding/json"
	"errors"

	stepstate "github.com/gorenx/goren/agentloop/internal/step"
	"github.com/gorenx/goren/session"
)

// ReconcileStepStart resolves a proposed start after its write request settles.
// The caller must read this complete snapshot from the same Session owner after
// the outstanding request ends. An older snapshot cannot prove non-commit.
// True means the Step committed and must be settled before its Turn can end.
func (subject *ReactLoopAgent) ReconcileStepStart(snapshot session.Snapshot) (bool, error) {
	if subject.preparing != nil || subject.step == nil || subject.step.StateValue() != stepstate.StateProposed {
		return false, errors.New("agentloop: Step start reconciliation requires an inactive preparation and unresolved candidate")
	}
	expected, err := subject.PreparePosition()
	if err != nil {
		return false, err
	}
	if snapshot.Barrier.SessionID != subject.identifier || snapshot.Barrier.NextSeq != int64(len(snapshot.Events)) {
		return false, errors.New("agentloop: Step start reconciliation requires a complete Session snapshot")
	}
	var turnNumber int64
	var lastStep int64
	var opened bool
	var seenTurn bool
	var matched *session.Event
	for index, entry := range snapshot.Events {
		if entry.Seq != int64(index) {
			return false, errors.New("agentloop: Step recovery Session sequence is discontinuous")
		}
		switch entry.Type {
		case session.TurnStartEventName:
			var started session.TurnStart
			if err := json.Unmarshal(entry.Data, &started); err != nil {
				return false, err
			}
			if started.Turn == expected.Turn {
				if seenTurn {
					return false, errors.New("agentloop: Step recovery found duplicate Turn starts")
				}
				seenTurn = true
			}
			turnNumber = started.Turn
			lastStep = 0
			opened = false
		case session.TurnEndEventName:
			turnNumber = 0
			opened = false
		case session.StepStartEventName, session.StepEndEventName:
			if turnNumber != expected.Turn {
				continue
			}
			var position session.StepPosition
			if err := json.Unmarshal(entry.Data, &position); err != nil {
				return false, err
			}
			if position.Turn != expected.Turn {
				return false, errors.New("agentloop: Step recovery found a foreign Turn boundary")
			}
			if entry.Type == session.StepStartEventName {
				if opened || position.Step != lastStep+1 || position.Step > expected.Step {
					return false, errors.New("agentloop: Step recovery found an unexpected start")
				}
				lastStep = position.Step
				opened = true
				if position == expected {
					if matched != nil {
						return false, errors.New("agentloop: Step recovery found duplicate starts")
					}
					copied := entry
					matched = &copied
				}
			} else {
				if !opened || position.Step != lastStep || position == expected {
					return false, errors.New("agentloop: Step recovery found an unexpected end")
				}
				opened = false
			}
		}
	}
	if turnNumber != expected.Turn {
		return false, errors.New("agentloop: Step recovery requires the current open Turn")
	}
	if matched != nil {
		if !opened || lastStep != expected.Step {
			return false, errors.New("agentloop: recovered Step is no longer current")
		}
		if err := subject.ConfirmStep(session.WriteResult{
			FirstSeq: matched.Seq,
			NextSeq:  matched.Seq + 1,
			Barrier: session.WriteBarrier{
				SessionID: subject.identifier,
				NextSeq:   matched.Seq + 1,
			},
			Events: []session.Event{*matched},
		}); err != nil {
			return false, err
		}
		return true, nil
	}
	if opened || lastStep != subject.current.LastStep() {
		return false, errors.New("agentloop: Step recovery snapshot does not reach the previous boundary")
	}
	subject.step = nil
	subject.preparing = nil
	return false, nil
}
