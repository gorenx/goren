package agentloop

import (
	"context"
	"testing"
)

func TestConstructionGateCloseBeforeOpen(t *testing.T) {
	t.Parallel()
	gate := newConstructionGate()
	if err := gate.closeAndWait(context.Background()); err != nil {
		t.Fatal(err)
	}
}
