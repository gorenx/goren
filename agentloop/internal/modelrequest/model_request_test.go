package modelrequest

import "testing"

func TestModelRequestCountsRetryAttempts(t *testing.T) {
	current := New()
	first, err := current.EnterStreaming()
	if err != nil {
		t.Fatal(err)
	}
	if first != 1 {
		t.Fatalf("first attempt = %d, want 1", first)
	}
	if err = current.EnterRetryPending(); err != nil {
		t.Fatal(err)
	}
	second, err := current.EnterStreaming()
	if err != nil {
		t.Fatal(err)
	}
	if second != 2 {
		t.Fatalf("second attempt = %d, want 2", second)
	}
	if err = current.EnterAccepted(); err != nil {
		t.Fatal(err)
	}
	if current.StateValue() != StateAccepted {
		t.Fatalf("state = %d, want accepted", current.StateValue())
	}
}

func TestModelRequestTerminalStateCannotReopen(t *testing.T) {
	current := New()
	if _, err := current.EnterStreaming(); err != nil {
		t.Fatal(err)
	}
	if err := current.EnterFailed(); err != nil {
		t.Fatal(err)
	}
	if _, err := current.EnterStreaming(); err == nil {
		t.Fatal("terminal ModelRequest dispatched another attempt")
	}
}

func TestModelRequestTransitionSourceMatrix(t *testing.T) {
	states := []State{
		StateProposed,
		StateStreaming,
		StateRetryPending,
		StateAccepted,
		StateFailed,
		StateAborted,
	}
	tests := []struct {
		name    string
		allowed map[State]bool
		apply   func(*ModelRequest) error
	}{
		{
			name: "EnterStreaming",
			allowed: map[State]bool{
				StateProposed:     true,
				StateRetryPending: true,
			},
			apply: func(current *ModelRequest) error {
				_, err := current.EnterStreaming()
				return err
			},
		},
		{
			name:    "EnterRetryPending",
			allowed: map[State]bool{StateStreaming: true},
			apply: func(current *ModelRequest) error {
				return current.EnterRetryPending()
			},
		},
		{
			name:    "EnterAccepted",
			allowed: map[State]bool{StateStreaming: true},
			apply:   func(current *ModelRequest) error { return current.EnterAccepted() },
		},
		{
			name: "EnterFailed",
			allowed: map[State]bool{
				StateProposed:     true,
				StateStreaming:    true,
				StateRetryPending: true,
			},
			apply: func(current *ModelRequest) error { return current.EnterFailed() },
		},
		{
			name: "EnterAborted",
			allowed: map[State]bool{
				StateProposed:     true,
				StateStreaming:    true,
				StateRetryPending: true,
			},
			apply: func(current *ModelRequest) error { return current.EnterAborted() },
		},
	}
	for _, testCase := range tests {
		for _, source := range states {
			t.Run(testCase.name+"/"+stateName(source), func(t *testing.T) {
				current := &ModelRequest{
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

func TestModelRequestAnyAcceptedTransitionPreservesAttemptInvariants(t *testing.T) {
	const operationCount = 5
	for encoded := 0; encoded < 3125; encoded++ {
		current := New()
		sequence := encoded
		previousAttempts := 0
		for depth := 0; depth < 5; depth++ {
			switch sequence % operationCount {
			case 0:
				_, _ = current.EnterStreaming()
			case 1:
				_ = current.EnterRetryPending()
			case 2:
				_ = current.EnterAccepted()
			case 3:
				_ = current.EnterFailed()
			case 4:
				_ = current.EnterAborted()
			}
			sequence /= operationCount
			if current.attempts < previousAttempts || current.attempts < 0 {
				t.Fatalf("attempt count regressed after sequence %d", encoded)
			}
			previousAttempts = current.attempts
			if isTerminal(current.currentState) {
				attemptCount := current.attempts
				_, _ = current.EnterStreaming()
				if current.attempts != attemptCount {
					t.Fatalf("terminal request reopened after sequence %d", encoded)
				}
			}
		}
	}
}

func isTerminal(selected State) bool {
	return selected == StateAccepted || selected == StateFailed ||
		selected == StateAborted
}

func stateName(selected State) string {
	switch selected {
	case StateProposed:
		return "proposed"
	case StateStreaming:
		return "streaming"
	case StateRetryPending:
		return "retry-pending"
	case StateAccepted:
		return "accepted"
	case StateFailed:
		return "failed"
	case StateAborted:
		return "aborted"
	default:
		return "unknown"
	}
}
