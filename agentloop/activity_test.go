package agentloop

import (
	"context"
	"strings"
	"testing"

	"github.com/gorenx/goren/session"
)

func TestRunMaintenanceRejectsLatchedSuccessorWake(t *testing.T) {
	t.Parallel()
	closedActivity := make(chan struct{})
	close(closedActivity)
	coordinator := &activityCoordinator{
		subject: &ReactLoopAgent{
			identifier: session.SessionID("maintenance-latched-wake"),
		},
		state: activityState{
			kind:          activityIdle,
			lastTurn:      3,
			wakeRequested: true,
		},
		activityDone: closedActivity,
		accepting:    true,
	}
	operationCalled := false
	err := coordinator.runMaintenance(
		context.Background(),
		func(context.Context) error {
			operationCalled = true
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "active or queued work") {
		t.Fatalf("maintenance error = %v", err)
	}
	if operationCalled {
		t.Fatal("maintenance operation ran ahead of a latched successor wake")
	}
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if coordinator.state.kind != activityIdle ||
		!coordinator.state.wakeRequested ||
		coordinator.state.lastTurn != 3 {
		t.Fatalf("activity state changed = %#v", coordinator.state)
	}
}
