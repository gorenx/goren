package lifecycle

import "testing"

func TestAgentLifecycleAdmissionsDefineCloseCutoff(t *testing.T) {
	owner := New()
	if err := owner.EnterServing(); err != nil {
		t.Fatal(err)
	}
	admitted, err := owner.AdmitInvocation()
	if err != nil {
		t.Fatal(err)
	}
	drained := owner.EnterClosing()
	select {
	case <-drained:
		t.Fatal("closing reported drained before admitted command released")
	default:
	}
	if _, err = owner.AdmitInvocation(); err == nil {
		t.Fatal("closing lifecycle admitted a new command")
	}
	if err = owner.FinishInvocation(admitted); err != nil {
		t.Fatal(err)
	}
	select {
	case <-drained:
	default:
		t.Fatal("closing did not observe the final invocation release")
	}
	if err = owner.EnterClosed(); err != nil {
		t.Fatal(err)
	}
	if owner.StateValue() != StateClosed {
		t.Fatalf("state = %d, want closed", owner.StateValue())
	}
}

func TestAgentLifecycleRejectsIllegalTransitions(t *testing.T) {
	owner := New()
	if _, err := owner.AdmitInvocation(); err == nil {
		t.Fatal("constructed lifecycle admitted a command")
	}
	if err := owner.EnterClosed(); err == nil {
		t.Fatal("constructed lifecycle finished closing")
	}
	if err := owner.EnterServing(); err != nil {
		t.Fatal(err)
	}
	if err := owner.EnterServing(); err == nil {
		t.Fatal("serving lifecycle began serving twice")
	}
	owner.EnterClosing()
	if err := owner.EnterClosed(); err != nil {
		t.Fatal(err)
	}
	if err := owner.EnterClosed(); err != nil {
		t.Fatalf("repeated finish closing = %v", err)
	}
}

func TestAgentLifecycleRejectsRepeatedCommandFinish(t *testing.T) {
	owner := New()
	if err := owner.EnterServing(); err != nil {
		t.Fatal(err)
	}
	admitted, err := owner.AdmitInvocation()
	if err != nil {
		t.Fatal(err)
	}
	if err = owner.FinishInvocation(admitted); err != nil {
		t.Fatal(err)
	}
	if err = owner.FinishInvocation(admitted); err == nil {
		t.Fatal("admitted command finished twice")
	}
}

func TestAgentLifecycleTransitionSourceMatrix(t *testing.T) {
	states := []State{
		StateConstructed,
		StateServing,
		StateClosing,
		StateClosed,
	}
	for _, source := range states {
		t.Run("EnterServing/"+stateName(source), func(t *testing.T) {
			owner := lifecycleAt(source)
			err := owner.EnterServing()
			if (err == nil) != (source == StateConstructed) {
				t.Fatalf("error = %v", err)
			}
		})
		t.Run("AdmitInvocation/"+stateName(source), func(t *testing.T) {
			owner := lifecycleAt(source)
			_, err := owner.AdmitInvocation()
			if (err == nil) != (source == StateServing) {
				t.Fatalf("error = %v", err)
			}
		})
		t.Run("EnterClosed/"+stateName(source), func(t *testing.T) {
			owner := lifecycleAt(source)
			err := owner.EnterClosed()
			allowed := source == StateClosing || source == StateClosed
			if (err == nil) != allowed {
				t.Fatalf("error = %v, allowed = %t", err, allowed)
			}
		})
	}
}

func TestAgentLifecycleFinishesInvocationBeforeAndAfterCutoff(t *testing.T) {
	for _, closeFirst := range []bool{false, true} {
		owner := New()
		if err := owner.EnterServing(); err != nil {
			t.Fatal(err)
		}
		invocation, err := owner.AdmitInvocation()
		if err != nil {
			t.Fatal(err)
		}
		if closeFirst {
			owner.EnterClosing()
		}
		if err = owner.FinishInvocation(invocation); err != nil {
			t.Fatalf("closeFirst=%t: %v", closeFirst, err)
		}
	}
}

func TestAgentLifecycleAnyAcceptedSequencePreservesCutoff(t *testing.T) {
	const operationCount = 5
	for encoded := 0; encoded < 3125; encoded++ {
		owner := New()
		invocations := make([]AgentInvocation, 0)
		sequence := encoded
		closingObserved := false
		for depth := 0; depth < 5; depth++ {
			switch sequence % operationCount {
			case 0:
				_ = owner.EnterServing()
			case 1:
				invocation, err := owner.AdmitInvocation()
				if err == nil {
					invocations = append(invocations, invocation)
				}
			case 2:
				owner.EnterClosing()
			case 3:
				if len(invocations) != 0 {
					invocation := invocations[0]
					if owner.FinishInvocation(invocation) == nil {
						invocations = invocations[1:]
					}
				}
			case 4:
				_ = owner.EnterClosed()
			}
			sequence /= operationCount
			selected := owner.StateValue()
			if selected == StateClosing || selected == StateClosed {
				closingObserved = true
			}
			if closingObserved &&
				(selected == StateConstructed || selected == StateServing) {
				t.Fatalf("closing cutoff reopened after sequence %d", encoded)
			}
			if selected == StateClosed && len(owner.activeInvocations) != 0 {
				t.Fatalf("closed with active invocations after sequence %d", encoded)
			}
		}
	}
}

func lifecycleAt(selected State) *AgentLifecycle {
	owner := New()
	owner.currentState = selected
	if selected == StateClosing || selected == StateClosed {
		owner.closeDrainedLocked()
	}
	return owner
}

func stateName(selected State) string {
	switch selected {
	case StateConstructed:
		return "constructed"
	case StateServing:
		return "serving"
	case StateClosing:
		return "closing"
	case StateClosed:
		return "closed"
	default:
		return "unknown"
	}
}
