package projectioncache

import "testing"

func TestLiveWriteCoalescesCheckpointTriggers(t *testing.T) {
	var write liveWrite
	shouldStart, _ := write.observe(false, 1)
	if !shouldStart {
		t.Fatal("first trigger did not start a writer")
	}
	firstAttempt := write.beginAttempt()
	if firstAttempt.kind != liveCheckpoint || firstAttempt.coveredEvents != 1 {
		t.Fatalf("first attempt = %#v", firstAttempt)
	}

	if shouldStart, _ := write.observe(true, 1); shouldStart {
		t.Fatal("trigger during I/O started a second writer")
	}
	if nextAction := write.finishAttempt(firstAttempt, true); nextAction != liveWriteContinue {
		t.Fatalf("first completion = %d", nextAction)
	}
	secondAttempt := write.beginAttempt()
	if secondAttempt.coveredEvents != 2 {
		t.Fatalf("second attempt = %#v", secondAttempt)
	}
	if nextAction := write.finishAttempt(secondAttempt, true); nextAction != liveWriteIdle {
		t.Fatalf("second completion = %d", nextAction)
	}
	if pending := write.pendingEvents(); pending != 0 {
		t.Fatalf("pending events = %d", pending)
	}
}

func TestLiveWriteDetachGetsOneFinalAttempt(t *testing.T) {
	var write liveWrite
	shouldStart, _ := write.observe(false, 1)
	if !shouldStart {
		t.Fatal("live trigger did not start a writer")
	}
	liveAttempt := write.beginAttempt()
	if write.detach() {
		t.Fatal("SessionDisposed started a second concurrent writer")
	}
	if nextAction := write.finishAttempt(liveAttempt, false); nextAction != liveWriteContinue {
		t.Fatalf("live failure completion = %d", nextAction)
	}
	finalAttempt := write.beginAttempt()
	if finalAttempt.kind != finalCheckpoint {
		t.Fatal("final attempt requested a second Session Flush")
	}
	if nextAction := write.finishAttempt(finalAttempt, false); nextAction != liveWriteRemove {
		t.Fatalf("final failure completion = %d", nextAction)
	}
}

func TestLiveWriteStopRemovesEnteredAttempt(t *testing.T) {
	var write liveWrite
	shouldStart, _ := write.observe(false, 1)
	if !shouldStart {
		t.Fatal("trigger did not start a writer")
	}
	writeAttempt := write.beginAttempt()
	write.stop()
	if nextAction := write.finishAttempt(writeAttempt, true); nextAction != liveWriteRemove {
		t.Fatalf("stale completion = %d", nextAction)
	}
}
