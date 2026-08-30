package execution

import (
	"context"
	"errors"
	"testing"
)

func TestAgentExecutionContinuesTurnWithoutIdleWindow(t *testing.T) {
	owner := New()
	firstContext, cancelFirst := context.WithCancelCause(context.Background())
	firstEntry, err := owner.EnterTurnOrRequestWake(firstContext, cancelFirst)
	if err != nil || !firstEntry.Entered {
		t.Fatalf("enter first Turn = %+v, %v", firstEntry, err)
	}
	first := firstEntry.Generation
	idle, done := owner.IdleWait()
	if idle {
		t.Fatal("running execution reported idle")
	}
	if err = owner.EnterTurnSettling(first); err != nil {
		t.Fatal(err)
	}
	secondContext, cancelSecond := context.WithCancelCause(context.Background())
	second, err := owner.EnterSuccessorTurn(first, secondContext, cancelSecond)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		t.Fatal("successor Turn closed the connected convergence signal")
	default:
	}
	if second == first {
		t.Fatal("successor Turn reused its execution generation")
	}
	if err = owner.EnterTurnSettling(second); err != nil {
		t.Fatal(err)
	}
	if err = owner.EnterIdle(second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	default:
		t.Fatal("final Turn did not close the convergence signal")
	}
}

func TestAgentExecutionKeepsFirstCancellation(t *testing.T) {
	owner := New()
	requestContext, cancelOperation := context.WithCancelCause(context.Background())
	entry, err := owner.EnterTurnOrRequestWake(requestContext, cancelOperation)
	if err != nil || !entry.Entered {
		t.Fatalf("enter Turn = %+v, %v", entry, err)
	}
	if owner.OperationContext(entry.Generation) != requestContext {
		t.Fatal("entered Turn did not retain its operation Context")
	}
	firstProblem := errors.New("first cancellation")
	owner.RecordCancellation(
		Cancellation{
			Kind: "user",
		},
		firstProblem,
		true,
	)
	owner.RecordCancellation(
		Cancellation{
			Kind: "disposed",
		},
		errors.New("second cancellation"),
		false,
	)
	executionView := owner.Snapshot()
	if executionView.Cancellation.Kind != "user" {
		t.Fatalf("cancellation = %q, want user", executionView.Cancellation.Kind)
	}
	if !errors.Is(context.Cause(requestContext), firstProblem) {
		t.Fatalf("operation cause = %v, want first cancellation", context.Cause(requestContext))
	}
}

func TestAgentExecutionLatchesWakeAcrossMaintenance(t *testing.T) {
	owner := New()
	maintenanceContext, cancelMaintenance := context.WithCancelCause(
		context.Background(),
	)
	selected, err := owner.EnterMaintenance(
		maintenanceContext,
		cancelMaintenance,
	)
	if err != nil {
		t.Fatal(err)
	}
	unusedContext, cancelUnused := context.WithCancelCause(context.Background())
	entry, err := owner.EnterTurnOrRequestWake(unusedContext, cancelUnused)
	cancelUnused(nil)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Entered {
		t.Fatal("maintenance admitted a concurrent Turn")
	}
	if !owner.WakeRequested(selected) {
		t.Fatal("maintenance did not latch the wake")
	}
	if err = owner.EnterMaintenanceSettling(selected); err != nil {
		t.Fatal(err)
	}
	turnContext, cancelTurn := context.WithCancelCause(context.Background())
	if _, err = owner.EnterTurnAfterMaintenance(
		selected,
		turnContext,
		cancelTurn,
	); err != nil {
		t.Fatal(err)
	}
}

func TestAgentExecutionTransitionSourceMatrix(t *testing.T) {
	states := []State{
		StateIdle,
		StateMaintenance,
		StateMaintenanceSettling,
		StateTurn,
		StateTurnSettling,
	}
	tests := []struct {
		name    string
		allowed map[State]bool
		apply   func(*AgentExecution) error
	}{
		{
			name:    "EnterMaintenance",
			allowed: map[State]bool{StateIdle: true},
			apply: func(owner *AgentExecution) error {
				runContext, cancelOperation := context.WithCancelCause(
					context.Background(),
				)
				_, err := owner.EnterMaintenance(runContext, cancelOperation)
				return err
			},
		},
		{
			name:    "EnterTurnSettling",
			allowed: map[State]bool{StateTurn: true},
			apply: func(owner *AgentExecution) error {
				return owner.EnterTurnSettling(1)
			},
		},
		{
			name:    "EnterMaintenanceSettling",
			allowed: map[State]bool{StateMaintenance: true},
			apply: func(owner *AgentExecution) error {
				return owner.EnterMaintenanceSettling(1)
			},
		},
		{
			name: "EnterIdle",
			allowed: map[State]bool{
				StateMaintenanceSettling: true,
				StateTurnSettling:        true,
			},
			apply: func(owner *AgentExecution) error {
				return owner.EnterIdle(1)
			},
		},
		{
			name:    "EnterSuccessorTurn",
			allowed: map[State]bool{StateTurnSettling: true},
			apply: func(owner *AgentExecution) error {
				runContext, cancelOperation := context.WithCancelCause(
					context.Background(),
				)
				_, err := owner.EnterSuccessorTurn(
					1,
					runContext,
					cancelOperation,
				)
				return err
			},
		},
		{
			name: "EnterTurnAfterMaintenance",
			allowed: map[State]bool{
				StateMaintenanceSettling: true,
			},
			apply: func(owner *AgentExecution) error {
				owner.wakeRequested = true
				runContext, cancelOperation := context.WithCancelCause(
					context.Background(),
				)
				_, err := owner.EnterTurnAfterMaintenance(
					1,
					runContext,
					cancelOperation,
				)
				return err
			},
		},
	}
	for _, testCase := range tests {
		for _, source := range states {
			t.Run(testCase.name+"/"+stateName(source), func(t *testing.T) {
				owner := executionAt(source)
				err := testCase.apply(owner)
				if (err == nil) != testCase.allowed[source] {
					t.Fatalf("error = %v, allowed = %t", err, testCase.allowed[source])
				}
			})
		}
	}
}

func TestAgentExecutionRejectsStaleGenerations(t *testing.T) {
	owner := executionAt(StateTurn)
	if err := owner.EnterTurnSettling(2); err == nil {
		t.Fatal("Turn settlement accepted a stale generation")
	}
	owner = executionAt(StateMaintenance)
	if err := owner.EnterMaintenanceSettling(2); err == nil {
		t.Fatal("maintenance settlement accepted a stale generation")
	}
	owner = executionAt(StateTurnSettling)
	if err := owner.EnterIdle(2); err == nil {
		t.Fatal("idle convergence accepted a stale generation")
	}
	if owner.OperationContext(2) != nil {
		t.Fatal("stale generation exposed an operation Context")
	}
}

func TestAgentExecutionCancellationBranches(t *testing.T) {
	owner := New()
	if owner.RecordCancellation(Cancellation{Kind: "user"}, errors.New("cancel"), true) {
		t.Fatal("idle execution accepted cancellation")
	}
	owner = executionAt(StateTurn)
	owner.wakeRequested = true
	if !owner.RecordCancellation(
		Cancellation{Kind: "disposed"},
		errors.New("disposed"),
		false,
	) {
		t.Fatal("active execution rejected cancellation")
	}
	if owner.wakeRequested {
		t.Fatal("disposal cancellation retained successor wake")
	}
}

func TestAgentExecutionAnyAcceptedSequencePreservesSlotInvariants(t *testing.T) {
	const operationCount = 6
	for encoded := 0; encoded < 7776; encoded++ {
		owner := New()
		sequence := encoded
		var previousGeneration Generation
		for depth := 0; depth < 5; depth++ {
			runContext := context.Background()
			cancelOperation := context.CancelCauseFunc(func(error) {})
			beforeSnapshot := owner.Snapshot()
			switch sequence % operationCount {
			case 0:
				_, _ = owner.EnterTurnOrRequestWake(
					runContext,
					cancelOperation,
				)
			case 1:
				_, _ = owner.EnterMaintenance(runContext, cancelOperation)
			case 2:
				_ = owner.EnterTurnSettling(beforeSnapshot.Generation)
			case 3:
				_ = owner.EnterMaintenanceSettling(beforeSnapshot.Generation)
			case 4:
				_, _ = owner.EnterSuccessorTurn(
					beforeSnapshot.Generation,
					runContext,
					cancelOperation,
				)
			case 5:
				_ = owner.EnterIdle(beforeSnapshot.Generation)
			}
			sequence /= operationCount
			currentSnapshot := owner.Snapshot()
			if currentSnapshot.Generation < previousGeneration {
				t.Fatalf("generation regressed after sequence %d", encoded)
			}
			previousGeneration = currentSnapshot.Generation
			if currentSnapshot.State == StateIdle &&
				owner.OperationContext(currentSnapshot.Generation) != nil {
				t.Fatalf("idle slot retained operation after sequence %d", encoded)
			}
		}
	}
}

func executionAt(selected State) *AgentExecution {
	owner := New()
	owner.currentState = selected
	if selected == StateIdle {
		return owner
	}
	runContext, cancelOperation := context.WithCancelCause(
		context.Background(),
	)
	owner.generation = 1
	owner.operation = runContext
	owner.cancel = cancelOperation
	owner.done = make(chan struct{})
	return owner
}

func stateName(selected State) string {
	switch selected {
	case StateIdle:
		return "idle"
	case StateMaintenance:
		return "maintenance"
	case StateMaintenanceSettling:
		return "maintenance-settling"
	case StateTurn:
		return "turn"
	case StateTurnSettling:
		return "turn-settling"
	default:
		return "unknown"
	}
}
