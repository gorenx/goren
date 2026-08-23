package projection

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/subagent"
)

const TimingKey = "subagentTiming"

// Interval is one open post-Descriptor turn interval.
type Interval struct {
	Since   int64 `json:"since"`
	Through int64 `json:"through"`
}

// Timing is the durable active-turn timing view.
type Timing struct {
	SettledMS int64     `json:"settledMs"`
	Active    *Interval `json:"active,omitempty"`
}

type timingState struct {
	SettledMS        int64     `json:"settledMs"`
	Active           *Interval `json:"active,omitempty"`
	PendingTurnStart *int64    `json:"pendingTurnStart,omitempty"`
	DescriptorSeen   bool      `json:"descriptorSeen"`
}

// TimingUnit folds turn boundaries around the child's own Descriptor.
type TimingUnit struct{}

func (TimingUnit) Key() string {
	return TimingKey
}

func (TimingUnit) StateVersion() int64 {
	return 2
}

func (TimingUnit) InitialState() (json.RawMessage, error) {
	return json.Marshal(timingState{})
}

func (TimingUnit) ApplyState(
	state json.RawMessage,
	committed session.Event,
) (sessionprojection.Transition, error) {
	var current timingState
	if err := decodeJSON(state, &current); err != nil {
		return sessionprojection.Transition{}, err
	}
	if err := validateTimingState(current); err != nil {
		return sessionprojection.Transition{}, err
	}
	next := cloneTimingState(current)
	switch committed.Type {
	case session.TurnStartEventName:
		if next.DescriptorSeen {
			next.Active = &Interval{
				Since:   committed.Time,
				Through: committed.Time,
			}
		} else {
			next.PendingTurnStart = int64Pointer(committed.Time)
		}
	case subagent.DescriptorEventName:
		var activeSince *int64
		if next.Active != nil {
			activeSince = int64Pointer(next.Active.Since)
		} else {
			activeSince = cloneInt64(next.PendingTurnStart)
		}
		next = timingState{
			DescriptorSeen: true,
		}
		if activeSince != nil {
			next.Active = &Interval{
				Since:   *activeSince,
				Through: committed.Time,
			}
		}
	case session.TurnEndEventName:
		if !next.DescriptorSeen {
			next.PendingTurnStart = nil
		} else if next.Active != nil {
			duration := committed.Time - next.Active.Since
			if duration > 0 {
				next.SettledMS += duration
			}
			next.Active = nil
		}
	default:
		if next.Active != nil {
			next.Active.Through = committed.Time
		}
	}
	nextState, err := json.Marshal(next)
	if err != nil {
		return sessionprojection.Transition{}, err
	}
	return sessionprojection.Transition{
		State:   nextState,
		Changed: !bytes.Equal(state, nextState),
	}, nil
}

func (TimingUnit) ViewState(state json.RawMessage) (json.RawMessage, error) {
	var current timingState
	if err := decodeJSON(state, &current); err != nil {
		return nil, err
	}
	if err := validateTimingState(current); err != nil {
		return nil, err
	}
	return json.Marshal(Timing{
		SettledMS: current.SettledMS,
		Active:    cloneInterval(current.Active),
	})
}

func validateTimingState(current timingState) error {
	if current.SettledMS < 0 ||
		(current.PendingTurnStart != nil && *current.PendingTurnStart < 0) ||
		(current.Active != nil &&
			(current.Active.Since < 0 || current.Active.Through < 0)) {
		return errors.New("subagent: invalid timing projection")
	}
	return nil
}

func cloneTimingState(source timingState) timingState {
	return timingState{
		SettledMS:        source.SettledMS,
		Active:           cloneInterval(source.Active),
		PendingTurnStart: cloneInt64(source.PendingTurnStart),
		DescriptorSeen:   source.DescriptorSeen,
	}
}

func cloneInterval(source *Interval) *Interval {
	if source == nil {
		return nil
	}
	snapshot := *source
	return &snapshot
}

func cloneInt64(source *int64) *int64 {
	if source == nil {
		return nil
	}
	return int64Pointer(*source)
}

func int64Pointer(value int64) *int64 {
	return &value
}

var _ sessionprojection.Unit = TimingUnit{}
