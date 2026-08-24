package basic

import (
	"context"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
)

func TestReadSurfaceRetriesUntilMeasurementAndSnapshotShareRevision(t *testing.T) {
	t.Parallel()
	conversation := conversationFixture(t, 2, "revision")
	measureCalls := 0
	pricing := &meterStub{
		measure: func(
			_ context.Context,
			current session.Context,
			_ *session.EpochHeader,
		) (tokenmeter.Measurement, error) {
			measureCalls++
			measured := pricedSurface(current, 100, 0)
			if measureCalls == 1 {
				messageValue, err := llm.NewUserMessage(llm.UserMessageInput{
					Content: []llm.ContentBlock{
						llm.NewTextBlock("written after pricing"),
					},
					Source: llm.UserMessageSource{},
				})
				if err != nil {
					return tokenmeter.Measurement{}, err
				}
				draft, err := session.NewSurfaceEventDraft(
					session.UserMessageAdded,
					messageValue,
					session.SurfaceIntent{
						Operation: session.SurfaceAppend(),
					},
				)
				if err != nil {
					return tokenmeter.Measurement{}, err
				}
				if _, err = current.Commit(
					context.Background(),
					session.Batch(draft),
				); err != nil {
					return tokenmeter.Measurement{}, err
				}
			}
			return measured, nil
		},
	}
	reading, err := readSurface(
		context.Background(),
		conversation,
		pricing,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if measureCalls != 2 {
		t.Fatalf("Measure calls = %d, want 2", measureCalls)
	}
	if reading.measurement.LogRevision != reading.snapshot.Barrier.NextSeq {
		t.Fatalf(
			"measurement revision = %d, snapshot revision = %d",
			reading.measurement.LogRevision,
			reading.snapshot.Barrier.NextSeq,
		)
	}
	if len(reading.measurement.Nodes) != len(reading.snapshot.Surface.Nodes) {
		t.Fatalf(
			"measured nodes = %d, Surface nodes = %d",
			len(reading.measurement.Nodes),
			len(reading.snapshot.Surface.Nodes),
		)
	}
}
