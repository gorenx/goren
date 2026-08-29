package turnrelay

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/gorenx/goren/session"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

const (
	cursorEventName = "subagent/bound-turn-relay-cursor"
	cursorVersion   = 1
)

// binding is the exact durable Bound relation reconstructed by this source.
// floor excludes the Binding event and every earlier parent event.
type binding struct {
	address boundcontract.Address
	childID session.SessionID
	floor   int64
}

type cursor struct {
	Version         int               `json:"version"`
	Name            string            `json:"name"`
	ChildSessionID  session.SessionID `json:"childSessionId"`
	PreviousNextSeq int64             `json:"previousNextSeq"`
	NextSeq         int64             `json:"nextSeq"`
	ThroughTurn     int64             `json:"throughTurn"`
	Delivered       bool              `json:"delivered"`
}

var cursorEvent = session.DefineEvent[cursor](cursorEventName)

func sessionBindings(
	events []session.Event,
	sessionID session.SessionID,
) ([]binding, error) {
	if sessionID == "" {
		return nil, errors.New(
			"subagent/bound/turnrelay: user Session ID is empty",
		)
	}
	result := make([]binding, 0)
	// Key is a Bound name. Value is the child Session ID already assigned to
	// that name in this user Session.
	names := make(map[string]session.SessionID)
	// Key is a child Session ID. Value is the Bound name already assigned to
	// that child in this user Session.
	children := make(map[session.SessionID]string)
	for _, committed := range events {
		if committed.Type != boundcontract.BindingEventName {
			continue
		}
		var recorded boundcontract.BindingData
		if err := turnCodec.Unmarshal(committed.Data, &recorded); err != nil {
			return nil, fmt.Errorf(
				"subagent/bound/turnrelay: decode Binding at seq %d: %w",
				committed.Seq,
				err,
			)
		}
		if recorded.Version != boundcontract.EventVersion ||
			recorded.Name == "" ||
			recorded.Name != strings.TrimSpace(recorded.Name) ||
			recorded.ChildSessionID == "" ||
			recorded.ContextNextSeq < 0 ||
			recorded.ContextNextSeq > committed.Seq {
			return nil, fmt.Errorf(
				"subagent/bound/turnrelay: invalid Binding at seq %d",
				committed.Seq,
			)
		}
		if _, duplicate := names[recorded.Name]; duplicate {
			return nil, errors.New(
				"subagent/bound/turnrelay: duplicate Bound name",
			)
		}
		if _, duplicate := children[recorded.ChildSessionID]; duplicate {
			return nil, errors.New(
				"subagent/bound/turnrelay: duplicate Bound child Session",
			)
		}
		names[recorded.Name] = recorded.ChildSessionID
		children[recorded.ChildSessionID] = recorded.Name
		result = append(result, binding{
			address: boundcontract.Address{
				SessionID: sessionID,
				Name:      recorded.Name,
			},
			childID: recorded.ChildSessionID,
			floor:   committed.Seq + 1,
		})
	}
	sort.Slice(result, func(leftIndex int, rightIndex int) bool {
		return result[leftIndex].address.Name < result[rightIndex].address.Name
	})
	return result, nil
}

func cursorPosition(
	events []session.Event,
	bindingValue binding,
) (int64, error) {
	nextSeq := bindingValue.floor
	for _, committed := range events {
		if committed.Type != cursorEventName {
			continue
		}
		var checkpoint cursor
		if err := turnCodec.Unmarshal(committed.Data, &checkpoint); err != nil {
			return 0, fmt.Errorf(
				"subagent/bound/turnrelay: decode cursor at seq %d: %w",
				committed.Seq,
				err,
			)
		}
		if checkpoint.Name != bindingValue.address.Name &&
			checkpoint.ChildSessionID != bindingValue.childID {
			continue
		}
		if checkpoint.Version != cursorVersion ||
			checkpoint.Name != bindingValue.address.Name ||
			checkpoint.ChildSessionID != bindingValue.childID ||
			checkpoint.PreviousNextSeq != nextSeq ||
			checkpoint.NextSeq <= checkpoint.PreviousNextSeq ||
			checkpoint.NextSeq > committed.Seq ||
			checkpoint.ThroughTurn <= 0 {
			return 0, fmt.Errorf(
				"subagent/bound/turnrelay: invalid cursor at seq %d",
				committed.Seq,
			)
		}
		nextSeq = checkpoint.NextSeq
	}
	return nextSeq, nil
}

type cursorPlan struct {
	binding     binding
	interaction interaction
	delivered   bool
}

func (plan cursorPlan) Build(
	_ context.Context,
	snapshot session.Snapshot,
) ([]session.EventDraft, error) {
	current, err := cursorPosition(snapshot.Events, plan.binding)
	if err != nil {
		return nil, err
	}
	if current == plan.interaction.nextSeq {
		return nil, nil
	}
	if current != plan.interaction.fromSeq {
		return nil, fmt.Errorf(
			"subagent/bound/turnrelay: cursor moved to %d while expecting %d",
			current,
			plan.interaction.fromSeq,
		)
	}
	draft, err := session.NewEventDraft(
		cursorEvent,
		cursor{
			Version:         cursorVersion,
			Name:            plan.binding.address.Name,
			ChildSessionID:  plan.binding.childID,
			PreviousNextSeq: plan.interaction.fromSeq,
			NextSeq:         plan.interaction.nextSeq,
			ThroughTurn:     plan.interaction.turn,
			Delivered:       plan.delivered,
		},
	)
	if err != nil {
		return nil, err
	}
	return []session.EventDraft{draft}, nil
}

var _ session.WritePlan = cursorPlan{}
