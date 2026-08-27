package seedbuilder

import (
	"context"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

// AppendDescriptor returns the SeedBuilder prefix followed by the durable
// Subagent descriptor event. The input events are never mutated.
func AppendDescriptor(
	childID session.SessionID,
	builderSeed []session.Event,
	descriptor subagent.Descriptor,
) ([]session.Event, error) {
	staged, err := session.New(
		childID,
		session.CreateOptions{
			Seed: builderSeed,
		},
	)
	if err != nil {
		return nil, err
	}
	descriptorData, err := subagent.SnapshotDescriptor(descriptor)
	if err != nil {
		return nil, err
	}
	draft, err := session.NewEventDraft(
		subagent.DescriptorEvent,
		descriptorData,
	)
	if err != nil {
		return nil, err
	}
	if _, err = staged.Commit(
		context.Background(),
		session.Batch(draft),
	); err != nil {
		return nil, err
	}
	return staged.Events(), nil
}
