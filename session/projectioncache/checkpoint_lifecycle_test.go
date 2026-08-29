package projectioncache

import "testing"

func TestCheckpointLifecycleCoalescesTriggers(t *testing.T) {
	var lifecycle checkpointLifecycle
	first := requireCheckpointCommand(
		t,
		captureCheckpointTransition(lifecycle.eventAppended(false, 1)),
		checkpointLifecycleStartCheckpoint,
	)
	if first.attempt.kind != liveCheckpoint ||
		first.attempt.coveredEvents != 1 ||
		first.attempt.id == 0 {
		t.Fatalf("first command = %#v", first)
	}

	requireCheckpointCommand(
		t,
		captureCheckpointTransition(lifecycle.eventAppended(true, 1)),
		checkpointLifecycleNoop,
	)
	assertCheckpointLifecycle(t, &lifecycle, checkpointLifecycleQueued, 2, 0)

	second := requireCheckpointCommand(
		t,
		captureCheckpointTransition(
			lifecycle.checkpointCompleted(first.attempt, checkpointSucceeded),
		),
		checkpointLifecycleStartCheckpoint,
	)
	if second.attempt.id == first.attempt.id ||
		second.attempt.coveredEvents != 2 {
		t.Fatalf("second command = %#v", second)
	}
	requireCheckpointCommand(
		t,
		captureCheckpointTransition(
			lifecycle.checkpointCompleted(second.attempt, checkpointSucceeded),
		),
		checkpointLifecycleNoop,
	)
	assertCheckpointLifecycle(t, &lifecycle, checkpointLifecycleClean, 2, 2)
}

func TestCheckpointLifecycleQueuesOnlyEventsAfterActiveAttempt(t *testing.T) {
	var lifecycle checkpointLifecycle
	active := requireCheckpointCommand(
		t,
		captureCheckpointTransition(lifecycle.eventAppended(true, 3)),
		checkpointLifecycleStartCheckpoint,
	)
	for index := 0; index < 2; index++ {
		requireCheckpointCommand(
			t,
			captureCheckpointTransition(lifecycle.eventAppended(false, 3)),
			checkpointLifecycleNoop,
		)
		assertCheckpointLifecycle(t, &lifecycle, checkpointLifecycleRunning, uint64(index+2), 0)
	}
	requireCheckpointCommand(
		t,
		captureCheckpointTransition(lifecycle.eventAppended(false, 3)),
		checkpointLifecycleNoop,
	)
	assertCheckpointLifecycle(t, &lifecycle, checkpointLifecycleQueued, 4, 0)
	if active.attempt.coveredEvents != 1 {
		t.Fatalf("active attempt = %#v", active.attempt)
	}
}

func TestCheckpointLifecycleDisposalGetsOneFinalAttempt(t *testing.T) {
	var lifecycle checkpointLifecycle
	live := requireCheckpointCommand(
		t,
		captureCheckpointTransition(lifecycle.eventAppended(false, 1)),
		checkpointLifecycleStartCheckpoint,
	)
	requireCheckpointCommand(
		t,
		captureCheckpointTransition(lifecycle.sessionDisposed()),
		checkpointLifecycleNoop,
	)
	assertCheckpointLifecycle(t, &lifecycle, checkpointLifecycleFinalQueued, 1, 0)

	final := requireCheckpointCommand(
		t,
		captureCheckpointTransition(
			lifecycle.checkpointCompleted(live.attempt, checkpointFailed),
		),
		checkpointLifecycleStartCheckpoint,
	)
	if final.attempt.kind != finalCheckpoint ||
		final.attempt.id == live.attempt.id {
		t.Fatalf("final command = %#v", final)
	}
	requireCheckpointCommand(
		t,
		captureCheckpointTransition(
			lifecycle.checkpointCompleted(final.attempt, checkpointFailed),
		),
		checkpointLifecycleRemove,
	)
	assertCheckpointLifecycle(t, &lifecycle, checkpointLifecycleClosed, 1, 0)
}

func TestCheckpointLifecycleUnavailableAttemptWaitsForDisposal(t *testing.T) {
	var lifecycle checkpointLifecycle
	live := requireCheckpointCommand(
		t,
		captureCheckpointTransition(lifecycle.eventAppended(false, 1)),
		checkpointLifecycleStartCheckpoint,
	)
	requireCheckpointCommand(
		t,
		captureCheckpointTransition(lifecycle.eventAppended(true, 1)),
		checkpointLifecycleNoop,
	)
	retry := requireCheckpointCommand(
		t,
		captureCheckpointTransition(
			lifecycle.checkpointCompleted(live.attempt, checkpointUnavailable),
		),
		checkpointLifecycleArmTimer,
	)
	if retry.timerGeneration == 0 {
		t.Fatal("unavailable attempt did not reserve a retry timer")
	}
	assertCheckpointLifecycle(t, &lifecycle, checkpointLifecycleDelayed, 2, 0)

	final := requireCheckpointCommand(
		t,
		captureCheckpointTransition(lifecycle.sessionDisposed()),
		checkpointLifecycleStartCheckpoint,
	)
	if final.attempt.kind != finalCheckpoint {
		t.Fatalf("disposal command = %#v", final)
	}
}

func TestCheckpointLifecycleRejectsStaleTimerAndCompletion(t *testing.T) {
	var lifecycle checkpointLifecycle
	firstTimer := requireCheckpointCommand(
		t,
		captureCheckpointTransition(lifecycle.eventAppended(false, 100)),
		checkpointLifecycleArmTimer,
	)
	active := requireCheckpointCommand(
		t,
		captureCheckpointTransition(lifecycle.eventAppended(true, 100)),
		checkpointLifecycleStartCheckpoint,
	)
	requireCheckpointCommand(
		t,
		captureCheckpointTransition(
			lifecycle.timerElapsed(firstTimer.timerGeneration),
		),
		checkpointLifecycleNoop,
	)

	stale := active.attempt
	stale.id++
	if _, err := lifecycle.checkpointCompleted(stale, checkpointSucceeded); err == nil {
		t.Fatal("stale checkpoint completion was accepted")
	}
	assertCheckpointLifecycle(t, &lifecycle, checkpointLifecycleRunning, 2, 0)
}

func TestCheckpointLifecycleCacheClosingDoesNotStartNewCheckpoint(t *testing.T) {
	var lifecycle checkpointLifecycle
	active := requireCheckpointCommand(
		t,
		captureCheckpointTransition(lifecycle.eventAppended(false, 1)),
		checkpointLifecycleStartCheckpoint,
	)
	requireCheckpointCommand(
		t,
		captureCheckpointTransition(lifecycle.cacheClosing()),
		checkpointLifecycleRemove,
	)
	requireCheckpointCommand(
		t,
		captureCheckpointTransition(
			lifecycle.checkpointCompleted(active.attempt, checkpointSucceeded),
		),
		checkpointLifecycleNoop,
	)
	assertCheckpointLifecycle(t, &lifecycle, checkpointLifecycleClosed, 1, 0)
}

func TestCheckpointLifecycleCacheClosingTerminatesEveryOpenState(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *checkpointLifecycle)
	}{
		{
			name: "clean",
			setup: func(*testing.T, *checkpointLifecycle) {
			},
		},
		{
			name: "delayed",
			setup: func(t *testing.T, lifecycle *checkpointLifecycle) {
				requireCheckpointCommand(
					t,
					captureCheckpointTransition(lifecycle.eventAppended(false, 100)),
					checkpointLifecycleArmTimer,
				)
			},
		},
		{
			name: "running",
			setup: func(t *testing.T, lifecycle *checkpointLifecycle) {
				requireCheckpointCommand(
					t,
					captureCheckpointTransition(lifecycle.eventAppended(false, 1)),
					checkpointLifecycleStartCheckpoint,
				)
			},
		},
		{
			name: "queued",
			setup: func(t *testing.T, lifecycle *checkpointLifecycle) {
				requireCheckpointCommand(
					t,
					captureCheckpointTransition(lifecycle.eventAppended(false, 1)),
					checkpointLifecycleStartCheckpoint,
				)
				requireCheckpointCommand(
					t,
					captureCheckpointTransition(lifecycle.eventAppended(true, 1)),
					checkpointLifecycleNoop,
				)
			},
		},
		{
			name: "final queued",
			setup: func(t *testing.T, lifecycle *checkpointLifecycle) {
				requireCheckpointCommand(
					t,
					captureCheckpointTransition(lifecycle.eventAppended(false, 1)),
					checkpointLifecycleStartCheckpoint,
				)
				requireCheckpointCommand(
					t,
					captureCheckpointTransition(lifecycle.sessionDisposed()),
					checkpointLifecycleNoop,
				)
			},
		},
		{
			name: "final running",
			setup: func(t *testing.T, lifecycle *checkpointLifecycle) {
				requireCheckpointCommand(
					t,
					captureCheckpointTransition(lifecycle.sessionDisposed()),
					checkpointLifecycleStartCheckpoint,
				)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var lifecycle checkpointLifecycle
			test.setup(t, &lifecycle)
			requireCheckpointCommand(
				t,
				captureCheckpointTransition(lifecycle.cacheClosing()),
				checkpointLifecycleRemove,
			)
			lifecycle.mutex.Lock()
			state := lifecycle.state
			attempt := lifecycle.activeAttempt
			timer := lifecycle.timer
			lifecycle.mutex.Unlock()
			if state != checkpointLifecycleClosed || attempt.id != 0 || timer != nil {
				t.Fatalf(
					"closed state = (%d, %#v, %v)",
					state,
					attempt,
					timer,
				)
			}
		})
	}
}

func TestCheckpointLifecycleRejectsInvalidState(t *testing.T) {
	lifecycle := checkpointLifecycle{
		state: checkpointLifecycleState(255),
	}
	if _, err := lifecycle.eventAppended(false, 1); err == nil {
		t.Fatal("unknown checkpoint lifecycle state was accepted")
	}
}

type capturedCheckpointTransition struct {
	command checkpointLifecycleCommand
	err     error
}

func captureCheckpointTransition(
	command checkpointLifecycleCommand,
	err error,
) capturedCheckpointTransition {
	return capturedCheckpointTransition{
		command: command,
		err:     err,
	}
}

func requireCheckpointCommand(
	t *testing.T,
	result capturedCheckpointTransition,
	want checkpointLifecycleCommandKind,
) checkpointLifecycleCommand {
	t.Helper()
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.command.kind != want {
		t.Fatalf(
			"command kind = %d, want %d; command = %#v",
			result.command.kind,
			want,
			result.command,
		)
	}
	return result.command
}

func assertCheckpointLifecycle(
	t *testing.T,
	lifecycle *checkpointLifecycle,
	want checkpointLifecycleState,
	observed uint64,
	checkpointed uint64,
) {
	t.Helper()
	lifecycle.mutex.Lock()
	defer lifecycle.mutex.Unlock()
	if lifecycle.state != want ||
		lifecycle.observedEvents != observed ||
		lifecycle.checkpointedEvents != checkpointed {
		t.Fatalf(
			"state = (%d, %d, %d), want (%d, %d, %d)",
			lifecycle.state,
			lifecycle.observedEvents,
			lifecycle.checkpointedEvents,
			want,
			observed,
			checkpointed,
		)
	}
}
