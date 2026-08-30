package step

import "testing"

func TestStepModelAndToolLifecycle(t *testing.T) {
	current, err := New(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = current.EnterOpen(); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterRequesting(); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterTooling(); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterSettling(StepResultContinue); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterClosed(); err != nil {
		t.Fatal(err)
	}
	if current.StateValue() != StateClosed {
		t.Fatalf("state = %d, want closed", current.StateValue())
	}
}

func TestStepRejectsToolsBeforeRequest(t *testing.T) {
	current, err := New(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = current.EnterOpen(); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterTooling(); err == nil {
		t.Fatal("Step accepted Tools before ModelRequest")
	}
}

func TestStepCanSettleAfterOpenedOperationFailure(t *testing.T) {
	current, err := New(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = current.EnterOpen(); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterSettling(StepResultError); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterClosed(); err != nil {
		t.Fatal(err)
	}
}

func TestStepTransitionSourceMatrix(t *testing.T) {
	states := []State{
		StateProposed,
		StateOpen,
		StateRequesting,
		StateTooling,
		StateSettling,
		StateClosed,
	}
	tests := []struct {
		name    string
		allowed map[State]bool
		apply   func(*Step) error
	}{
		{
			name:    "EnterOpen",
			allowed: map[State]bool{StateProposed: true},
			apply:   func(current *Step) error { return current.EnterOpen() },
		},
		{
			name:    "EnterRequesting",
			allowed: map[State]bool{StateOpen: true},
			apply:   func(current *Step) error { return current.EnterRequesting() },
		},
		{
			name:    "EnterTooling",
			allowed: map[State]bool{StateRequesting: true},
			apply:   func(current *Step) error { return current.EnterTooling() },
		},
		{
			name: "EnterSettling",
			allowed: map[State]bool{
				StateOpen:       true,
				StateRequesting: true,
				StateTooling:    true,
			},
			apply: func(current *Step) error {
				return current.EnterSettling(StepResultAborted)
			},
		},
		{
			name:    "EnterClosed",
			allowed: map[State]bool{StateSettling: true},
			apply:   func(current *Step) error { return current.EnterClosed() },
		},
	}
	for _, testCase := range tests {
		for _, source := range states {
			t.Run(testCase.name+"/"+stateName(source), func(t *testing.T) {
				current := &Step{
					turnNumber:   1,
					stepNumber:   1,
					currentState: source,
				}
				err := testCase.apply(current)
				if (err == nil) != testCase.allowed[source] {
					t.Fatalf("error = %v, allowed = %t", err, testCase.allowed[source])
				}
			})
		}
	}
}

func TestStepAnyAcceptedTransitionPreservesBoundaryInvariants(t *testing.T) {
	const operationCount = 6
	for encoded := 0; encoded < 7776; encoded++ {
		current, err := New(1, 1)
		if err != nil {
			t.Fatal(err)
		}
		sequence := encoded
		for depth := 0; depth < 5; depth++ {
			switch sequence % operationCount {
			case 0:
				_ = current.EnterOpen()
			case 1:
				_ = current.EnterRequesting()
			case 2:
				_ = current.EnterTooling()
			case 3:
				_ = current.EnterSettling(StepResultContinue)
			case 4:
				_ = current.EnterSettling(StepResultAborted)
			case 5:
				_ = current.EnterClosed()
			}
			sequence /= operationCount
			if current.currentState == StateClosed &&
				current.conclusion == StepResultNone {
				t.Fatalf("closed Step has no result after sequence %d", encoded)
			}
		}
	}
}

func stateName(selected State) string {
	switch selected {
	case StateProposed:
		return "proposed"
	case StateOpen:
		return "open"
	case StateRequesting:
		return "requesting"
	case StateTooling:
		return "tooling"
	case StateSettling:
		return "settling"
	case StateClosed:
		return "closed"
	default:
		return "unknown"
	}
}
