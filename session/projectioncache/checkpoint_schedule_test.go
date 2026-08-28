package projectioncache

import "testing"

func TestCheckpointScheduleCoalescesLiveTriggers(t *testing.T) {
	var schedule checkpointSchedule
	schedule.observe()
	if !schedule.requestCheckpoint() {
		t.Fatal("first trigger did not start a writer")
	}
	firstAttempt := schedule.beginAttempt()
	if !firstAttempt.requiresLiveFlush() || firstAttempt.coveredEvents != 1 {
		t.Fatalf("first attempt = %#v", firstAttempt)
	}

	schedule.observe()
	if schedule.requestCheckpoint() {
		t.Fatal("trigger during I/O started a second writer")
	}
	if nextAction := schedule.finishAttempt(firstAttempt, true, true); nextAction != checkpointContinue {
		t.Fatalf("first completion = %d", nextAction)
	}
	secondAttempt := schedule.beginAttempt()
	if secondAttempt.coveredEvents != 2 {
		t.Fatalf("second attempt = %#v", secondAttempt)
	}
	if nextAction := schedule.finishAttempt(secondAttempt, true, true); nextAction != checkpointIdle {
		t.Fatalf("second completion = %d", nextAction)
	}
	if pending := schedule.pendingEvents(); pending != 0 {
		t.Fatalf("pending events = %d", pending)
	}
}

func TestCheckpointScheduleRetireGetsOneFinalAttempt(t *testing.T) {
	var schedule checkpointSchedule
	schedule.observe()
	if !schedule.requestCheckpoint() {
		t.Fatal("live trigger did not start a writer")
	}
	liveAttempt := schedule.beginAttempt()
	schedule.markRetiring()
	if schedule.requestCheckpoint() {
		t.Fatal("Retire started a second concurrent writer")
	}
	if nextAction := schedule.finishAttempt(liveAttempt, false, true); nextAction != checkpointContinue {
		t.Fatalf("live failure completion = %d", nextAction)
	}
	finalAttempt := schedule.beginAttempt()
	if finalAttempt.requiresLiveFlush() {
		t.Fatal("final attempt requested a second Session Flush")
	}
	if nextAction := schedule.finishAttempt(finalAttempt, false, true); nextAction != checkpointRemove {
		t.Fatalf("final failure completion = %d", nextAction)
	}
}

func TestCheckpointScheduleRemovesStaleLifecycle(t *testing.T) {
	var schedule checkpointSchedule
	schedule.observe()
	if !schedule.requestCheckpoint() {
		t.Fatal("trigger did not start a writer")
	}
	writeAttempt := schedule.beginAttempt()
	if nextAction := schedule.finishAttempt(writeAttempt, true, false); nextAction != checkpointRemove {
		t.Fatalf("stale completion = %d", nextAction)
	}
}
