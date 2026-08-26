package seedbuilder

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/subagent"
)

type builderRecord struct {
	name string
}

func (record builderRecord) Name() string {
	return record.name
}

func (builderRecord) Policy() subagent.SeedPolicy {
	return subagent.SeedPolicy{}
}

func (builderRecord) BuildSeed(
	context.Context,
	subagent.SeedRequest,
) (subagent.SessionSeed, error) {
	return subagent.SessionSeed{}, nil
}

type eventRecord struct {
	addedErr error
	added    []string
	removed  []string
}

func (record *eventRecord) Added(
	_ context.Context,
	builder subagent.SeedBuilder,
) error {
	record.added = append(record.added, builder.Name())
	return record.addedErr
}

func (record *eventRecord) Removed(
	_ context.Context,
	candidateName string,
) {
	record.removed = append(record.removed, candidateName)
}

func TestRegistrationVetoRollsBackAndCloseIsStable(t *testing.T) {
	eventLog := &eventRecord{
		addedErr: errors.New("veto"),
	}
	owner := New(eventLog)
	_, err := owner.Register(
		context.Background(),
		builderRecord{name: "spawn"},
	)
	if !errors.Is(err, eventLog.addedErr) {
		t.Fatalf("registration error = %v", err)
	}
	if _, found := owner.Find("spawn"); found {
		t.Fatal("vetoed SeedBuilder remained registered")
	}
	eventLog.addedErr = nil
	handle, err := owner.Register(
		context.Background(),
		builderRecord{name: "spawn"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Unregister(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := handle.Unregister(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(eventLog.removed) != 1 || eventLog.removed[0] != "spawn" {
		t.Fatalf("removed events = %#v", eventLog.removed)
	}
}
