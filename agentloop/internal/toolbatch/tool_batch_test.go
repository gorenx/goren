package toolbatch

import "testing"

func TestToolBatchAcceptsResultsInModelOrder(t *testing.T) {
	current, err := New(2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err = current.EnterDispatching(); err != nil {
		t.Fatal(err)
	}
	if err = current.RecordCallStart(0); err != nil {
		t.Fatal(err)
	}
	if err = current.RecordCallStart(1); err != nil {
		t.Fatal(err)
	}
	if err = current.RecordCallSettlement(1); err != nil {
		t.Fatal(err)
	}
	if err = current.RecordAcceptedResult(1, false); err == nil {
		t.Fatal("ToolBatch accepted an out-of-order result")
	}
	if err = current.RecordCallSettlement(0); err != nil {
		t.Fatal(err)
	}
	if err = current.RecordAcceptedResult(0, false); err != nil {
		t.Fatal(err)
	}
	if err = current.RecordAcceptedResult(1, true); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterSettling(); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterClosed(); err != nil {
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
	if err = current.EnterDispatching(); err != nil {
		t.Fatal(err)
	}
	if err = current.RecordCallStart(0); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterDraining(DrainCancellation); err != nil {
		t.Fatal(err)
	}
	if err = current.RecordCallSettlement(0); err != nil {
		t.Fatal(err)
	}
	if err = current.RecordAcceptedResult(0, false); err != nil {
		t.Fatal(err)
	}
	if err = current.RecordSkippedResult(1); err != nil {
		t.Fatal(err)
	}
	if err = current.RecordSkippedResult(2); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterSettling(); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterClosed(); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerFailureDoesNotRequireSyntheticPairs(t *testing.T) {
	current, err := New(2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = current.EnterDispatching(); err != nil {
		t.Fatal(err)
	}
	if err = current.RecordCallStart(0); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterDraining(DrainFailure); err != nil {
		t.Fatal(err)
	}
	if err = current.RecordCallSettlement(0); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterSettling(); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterClosed(); err != nil {
		t.Fatal(err)
	}
}

func TestToolBatchTransitionSourceMatrix(t *testing.T) {
	states := []State{
		StatePlanned,
		StateDispatching,
		StateDraining,
		StateSettling,
		StateClosed,
	}
	tests := []struct {
		name    string
		allowed map[State]bool
		apply   func(*ToolBatch) error
	}{
		{
			name:    "EnterDispatching",
			allowed: map[State]bool{StatePlanned: true},
			apply:   func(current *ToolBatch) error { return current.EnterDispatching() },
		},
		{
			name:    "RecordCallStart",
			allowed: map[State]bool{StateDispatching: true},
			apply: func(current *ToolBatch) error {
				return current.RecordCallStart(0)
			},
		},
		{
			name: "RecordCallSettlement",
			allowed: map[State]bool{
				StateDispatching: true,
				StateDraining:    true,
			},
			apply: func(current *ToolBatch) error {
				current.started[0] = true
				current.nextToStart = 1
				current.inFlight = 1
				return current.RecordCallSettlement(0)
			},
		},
		{
			name: "RecordAcceptedResult",
			allowed: map[State]bool{
				StateDispatching: true,
				StateDraining:    true,
			},
			apply: func(current *ToolBatch) error {
				current.started[0] = true
				current.settled[0] = true
				current.nextToStart = 1
				return current.RecordAcceptedResult(0, false)
			},
		},
		{
			name:    "EnterDraining",
			allowed: map[State]bool{StateDispatching: true},
			apply: func(current *ToolBatch) error {
				return current.EnterDraining(DrainFailure)
			},
		},
		{
			name:    "RecordSkippedResult",
			allowed: map[State]bool{StateDraining: true},
			apply: func(current *ToolBatch) error {
				current.drainReason = DrainCancellation
				return current.RecordSkippedResult(0)
			},
		},
		{
			name: "EnterSettling",
			allowed: map[State]bool{
				StateDispatching: true,
				StateDraining:    true,
			},
			apply: func(current *ToolBatch) error {
				current.nextToStart = 1
				current.nextToAccept = 1
				current.started[0] = true
				current.settled[0] = true
				current.accepted[0] = true
				return current.EnterSettling()
			},
		},
		{
			name:    "EnterClosed",
			allowed: map[State]bool{StateSettling: true},
			apply:   func(current *ToolBatch) error { return current.EnterClosed() },
		},
	}
	for _, testCase := range tests {
		for _, source := range states {
			t.Run(testCase.name+"/"+stateName(source), func(t *testing.T) {
				current, err := New(1, 1)
				if err != nil {
					t.Fatal(err)
				}
				current.currentState = source
				err = testCase.apply(current)
				if (err == nil) != testCase.allowed[source] {
					t.Fatalf("error = %v, allowed = %t", err, testCase.allowed[source])
				}
			})
		}
	}
}

func TestToolBatchAnyAcceptedTransitionPreservesOrdering(t *testing.T) {
	const operationCount = 8
	for encoded := 0; encoded < 32768; encoded++ {
		current, err := New(2, 1)
		if err != nil {
			t.Fatal(err)
		}
		sequence := encoded
		for depth := 0; depth < 5; depth++ {
			switch sequence % operationCount {
			case 0:
				_ = current.EnterDispatching()
			case 1:
				_ = current.RecordCallStart(current.nextToStart)
			case 2:
				_ = current.RecordCallSettlement(current.nextToAccept)
			case 3:
				_ = current.RecordAcceptedResult(current.nextToAccept, false)
			case 4:
				_ = current.EnterDraining(DrainCancellation)
			case 5:
				_ = current.RecordSkippedResult(current.nextToAccept)
			case 6:
				_ = current.EnterSettling()
			case 7:
				_ = current.EnterClosed()
			}
			sequence /= operationCount
			if current.nextToAccept > current.nextToStart ||
				current.nextToStart > len(current.started) || current.inFlight < 0 {
				t.Fatalf("ordering invariant failed after sequence %d", encoded)
			}
			if current.currentState == StateClosed && current.inFlight != 0 {
				t.Fatalf("closed with in-flight calls after sequence %d", encoded)
			}
		}
	}
}

func stateName(selected State) string {
	switch selected {
	case StatePlanned:
		return "planned"
	case StateDispatching:
		return "dispatching"
	case StateDraining:
		return "draining"
	case StateSettling:
		return "settling"
	case StateClosed:
		return "closed"
	default:
		return "unknown"
	}
}
