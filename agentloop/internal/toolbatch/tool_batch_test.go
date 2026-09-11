package toolbatch

import "testing"

func TestToolBatchAcceptsResultsInModelOrder(t *testing.T) {
	current, err := New(2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err = current.StartCall(0); err != nil {
		t.Fatal(err)
	}
	if err = current.StartCall(1); err != nil {
		t.Fatal(err)
	}
	if err = current.CompleteCall(1); err != nil {
		t.Fatal(err)
	}
	if err = current.AcceptResult(1, false); err == nil {
		t.Fatal("ToolBatch accepted an out-of-order result")
	}
	if err = current.CompleteCall(0); err != nil {
		t.Fatal(err)
	}
	if err = current.AcceptResult(0, false); err != nil {
		t.Fatal(err)
	}
	if err = current.AcceptResult(1, true); err != nil {
		t.Fatal(err)
	}
	if err = current.Complete(); err != nil {
		t.Fatal(err)
	}
	if !current.StopsModelContinuation() {
		t.Fatal("accepted model-continuation stop was lost")
	}
}

func TestCanceledToolBatchRequiresSyntheticPairs(t *testing.T) {
	current, err := New(3, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = current.StartCall(0); err != nil {
		t.Fatal(err)
	}
	if err = current.StopDispatch(Canceled); err != nil {
		t.Fatal(err)
	}
	if err = current.CompleteCall(0); err != nil {
		t.Fatal(err)
	}
	if err = current.AcceptResult(0, false); err != nil {
		t.Fatal(err)
	}
	if err = current.AcceptCancellation(1); err != nil {
		t.Fatal(err)
	}
	if err = current.AcceptCancellation(2); err != nil {
		t.Fatal(err)
	}
	if err := current.CompleteCall(1); err == nil {
		t.Fatal("synthetic cancellation accepted a call exit")
	}
	if current.inFlight != 0 {
		t.Fatal("rejected call exit changed the in-flight count")
	}
	if err = current.Complete(); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerFailureDoesNotRequireSyntheticPairs(t *testing.T) {
	current, err := New(2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = current.StartCall(0); err != nil {
		t.Fatal(err)
	}
	if err = current.StopDispatch(ExecutionFailed); err != nil {
		t.Fatal(err)
	}
	if err = current.CompleteCall(0); err != nil {
		t.Fatal(err)
	}
	if err = current.Complete(); err != nil {
		t.Fatal(err)
	}
}

func TestToolBatchAnyAcceptedTransitionPreservesOrdering(t *testing.T) {
	const operationCount = 6
	for encoded := 0; encoded < 7776; encoded++ {
		current, err := New(2, 1)
		if err != nil {
			t.Fatal(err)
		}
		sequence := encoded
		for depth := 0; depth < 5; depth++ {
			switch sequence % operationCount {
			case 0:
				_ = current.StartCall(current.nextToStart)
			case 1:
				_ = current.CompleteCall(current.nextToAccept)
			case 2:
				_ = current.AcceptResult(current.nextToAccept, false)
			case 3:
				_ = current.StopDispatch(Canceled)
			case 4:
				_ = current.AcceptCancellation(current.nextToAccept)
			case 5:
				_ = current.Complete()
			}
			sequence /= operationCount
			if current.nextToAccept > current.nextToStart ||
				current.nextToStart > len(current.finished) || current.inFlight < 0 {
				t.Fatalf("ordering invariant failed after sequence %d", encoded)
			}
			if current.IsComplete() && current.inFlight != 0 {
				t.Fatalf("closed with in-flight calls after sequence %d", encoded)
			}
		}
	}
}

func TestToolBatchDispatchHonorsCapacityAndCompletion(t *testing.T) {
	current, err := New(2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !current.CanStart() {
		t.Fatal("new batch cannot dispatch")
	}
	if err := current.StartCall(0); err != nil {
		t.Fatal(err)
	}
	if current.CanStart() {
		t.Fatal("parallel limit did not stop dispatch")
	}
	if err := current.Complete(); err == nil {
		t.Fatal("completed with a running call")
	}
	if err := current.CompleteCall(0); err != nil {
		t.Fatal(err)
	}
	if !current.CanStart() {
		t.Fatal("call exit did not release capacity")
	}
	if err := current.AcceptResult(0, false); err != nil {
		t.Fatal(err)
	}
	if err := current.StartCall(1); err != nil {
		t.Fatal(err)
	}
	if err := current.CompleteCall(1); err != nil {
		t.Fatal(err)
	}
	if err := current.AcceptResult(1, false); err != nil {
		t.Fatal(err)
	}
	if err := current.Complete(); err != nil {
		t.Fatal(err)
	}
	if !current.IsComplete() || current.CanStart() {
		t.Fatal("completed batch permits dispatch")
	}
	if err := current.StopDispatch(Canceled); err == nil {
		t.Fatal("completed batch changed its stop reason")
	}
}
