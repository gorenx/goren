package turn

import "testing"

func TestTurnKeepsMaxTokensStickyAcrossLaterSteps(t *testing.T) {
	current, err := New(1)
	if err != nil {
		t.Fatal(err)
	}
	if err = current.EnterOpen(); err != nil {
		t.Fatal(err)
	}
	first, err := current.ProposedStep()
	if err != nil {
		t.Fatal(err)
	}
	if err = current.EnterStepping(first); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterOpenAfterStep(TurnResultMaxTokens); err != nil {
		t.Fatal(err)
	}
	second, err := current.ProposedStep()
	if err != nil {
		t.Fatal(err)
	}
	if err = current.EnterStepping(second); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterOpenAfterStep(TurnResultCompleted); err != nil {
		t.Fatal(err)
	}
	if current.ResultValue() != TurnResultMaxTokens {
		t.Fatalf("result = %d, want max-tokens", current.ResultValue())
	}
	if err = current.EnterStopping(); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterSettling(current.ResultValue()); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterClosed(); err != nil {
		t.Fatal(err)
	}
}

func TestTurnRejectsSkippedStepNumber(t *testing.T) {
	current, err := New(1)
	if err != nil {
		t.Fatal(err)
	}
	if err = current.EnterOpen(); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterStepping(2); err == nil {
		t.Fatal("Turn accepted a skipped Step number")
	}
}

func TestTurnAllowsStoppingHookContinuation(t *testing.T) {
	current, err := New(1)
	if err != nil {
		t.Fatal(err)
	}
	if err = current.EnterOpen(); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterStepping(1); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterOpenAfterStep(TurnResultCompleted); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterStopping(); err != nil {
		t.Fatal(err)
	}
	if err = current.EnterOpenAfterStopping(); err != nil {
		t.Fatal(err)
	}
	if stepNumber, proposalErr := current.ProposedStep(); proposalErr != nil || stepNumber != 2 {
		t.Fatalf("proposed Step = %d, %v; want 2", stepNumber, proposalErr)
	}
}

func TestTurnTransitionSourceMatrix(t *testing.T) {
	states := []State{
		StateProposed,
		StateOpen,
		StateStepping,
		StateStopping,
		StateSettling,
		StateClosed,
	}
	tests := []struct {
		name    string
		allowed map[State]bool
		apply   func(*Turn) error
	}{
		{
			name:    "EnterOpen",
			allowed: map[State]bool{StateProposed: true},
			apply:   func(current *Turn) error { return current.EnterOpen() },
		},
		{
			name:    "ProposedStep",
			allowed: map[State]bool{StateOpen: true},
			apply: func(current *Turn) error {
				_, err := current.ProposedStep()
				return err
			},
		},
		{
			name:    "EnterStepping",
			allowed: map[State]bool{StateOpen: true},
			apply:   func(current *Turn) error { return current.EnterStepping(1) },
		},
		{
			name:    "EnterOpenAfterStep",
			allowed: map[State]bool{StateStepping: true},
			apply: func(current *Turn) error {
				return current.EnterOpenAfterStep(TurnResultCompleted)
			},
		},
		{
			name:    "EnterStopping",
			allowed: map[State]bool{StateOpen: true},
			apply:   func(current *Turn) error { return current.EnterStopping() },
		},
		{
			name:    "EnterOpenAfterStopping",
			allowed: map[State]bool{StateStopping: true},
			apply: func(current *Turn) error {
				return current.EnterOpenAfterStopping()
			},
		},
		{
			name: "EnterSettling",
			allowed: map[State]bool{
				StateOpen:     true,
				StateStepping: true,
				StateStopping: true,
			},
			apply: func(current *Turn) error {
				return current.EnterSettling(TurnResultError)
			},
		},
		{
			name:    "EnterClosed",
			allowed: map[State]bool{StateSettling: true},
			apply:   func(current *Turn) error { return current.EnterClosed() },
		},
	}
	for _, testCase := range tests {
		for _, source := range states {
			t.Run(testCase.name+"/"+stateName(source), func(t *testing.T) {
				current := &Turn{
					number:       1,
					currentState: source,
					conclusion:   TurnResultCompleted,
				}
				err := testCase.apply(current)
				if (err == nil) != testCase.allowed[source] {
					t.Fatalf("error = %v, allowed = %t", err, testCase.allowed[source])
				}
			})
		}
	}
}

func TestTurnAnyAcceptedTransitionPreservesBoundaryInvariants(t *testing.T) {
	const operationCount = 7
	for encoded := 0; encoded < 16807; encoded++ {
		current, err := New(1)
		if err != nil {
			t.Fatal(err)
		}
		sequence := encoded
		for depth := 0; depth < 5; depth++ {
			switch sequence % operationCount {
			case 0:
				_ = current.EnterOpen()
			case 1:
				_ = current.EnterStepping(current.lastStep + 1)
			case 2:
				_ = current.EnterOpenAfterStep(TurnResultContinue)
			case 3:
				_ = current.EnterOpenAfterStep(TurnResultMaxTokens)
			case 4:
				_ = current.EnterStopping()
			case 5:
				_ = current.EnterSettling(TurnResultAborted)
			case 6:
				_ = current.EnterClosed()
			}
			sequence /= operationCount
			if current.lastStep < 0 {
				t.Fatalf("negative Step after sequence %d", encoded)
			}
			if current.currentState == StateClosed &&
				current.conclusion == TurnResultNone {
				t.Fatalf("closed Turn has no result after sequence %d", encoded)
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
	case StateStepping:
		return "stepping"
	case StateStopping:
		return "stopping"
	case StateSettling:
		return "settling"
	case StateClosed:
		return "closed"
	default:
		return "unknown"
	}
}
