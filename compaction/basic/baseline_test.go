package basic

import (
	"testing"

	"github.com/gorenx/goren/compaction"
)

func TestBaselineCapturesPricesAndSurfaceWithoutRetainingCallerSlices(t *testing.T) {
	snapshot, measurement := selectionSnapshot(t, 4)
	source, err := captureBaseline(snapshot, measurement, compaction.SurfaceRange{
		Start: 0,
		End:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if source.tokenCount != 20 || len(source.input.messages) != 2 {
		t.Fatalf("baseline = %#v", source)
	}
	measurement.Nodes[0].Tokens = 999
	snapshot.Surface.Nodes[0] = 3
	if source.tokenCount != 20 || source.surface[0] != 0 || source.selected.sequences[0] != 0 {
		t.Fatal("baseline shares caller values")
	}
	if err := assertWholeSurfaceStable(snapshot, source); err == nil {
		t.Fatal("changed Surface accepted")
	}
}

func TestBaselineAllowsOutsideChangesOnlyForSelectedRegionStability(t *testing.T) {
	snapshot, measurement := selectionSnapshot(t, 4)
	source, err := captureBaseline(snapshot, measurement, compaction.SurfaceRange{
		Start: 0,
		End:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Surface.Nodes = []int64{0, 1, 3, 2}
	if err := assertSelectedRegionStable(snapshot, source); err != nil {
		t.Fatal(err)
	}
	if err := assertWholeSurfaceStable(snapshot, source); err == nil {
		t.Fatal("automatic mode accepted outside changes")
	}
	snapshot.Surface.Nodes = []int64{0, 2, 1, 3}
	if err := assertSelectedRegionStable(snapshot, source); err == nil {
		t.Fatal("changed selection accepted")
	}
}
