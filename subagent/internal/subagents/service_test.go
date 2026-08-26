package subagents

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

type implementationRecord struct {
	mode         subagent.Mode
	startEntered chan struct{}
	startRelease chan struct{}
	closeOrder   *[]subagent.Mode
	closeMutex   *sync.Mutex
}

func (record *implementationRecord) Mode() subagent.Mode {
	return record.mode
}

func (record *implementationRecord) Start(
	context.Context,
	subagent.StartCommand,
) (subagent.Execution, error) {
	if record.startEntered != nil {
		close(record.startEntered)
	}
	if record.startRelease != nil {
		<-record.startRelease
	}
	return nil, nil
}

func (*implementationRecord) Interrupt(
	context.Context,
	session.SessionID,
) error {
	return nil
}

func (record *implementationRecord) Close(context.Context) error {
	if record.closeOrder == nil {
		return nil
	}
	record.closeMutex.Lock()
	*record.closeOrder = append(*record.closeOrder, record.mode)
	record.closeMutex.Unlock()
	return nil
}

type continuableRecord struct {
	*implementationRecord
}

func (*continuableRecord) Send(
	context.Context,
	agent.Agent,
	session.SessionID,
	[]llm.ContentBlock,
	subagent.FollowupOptions,
) (llm.MessageID, error) {
	return "", nil
}

func (*continuableRecord) Report(
	context.Context,
	agent.Agent,
	[]llm.ContentBlock,
	subagent.ReportOptions,
) (llm.MessageID, error) {
	return "", nil
}

func TestServiceCloseStopsAdmissionAndWaitsForAdmittedStart(t *testing.T) {
	t.Parallel()
	closeOrder := make([]subagent.Mode, 0, 2)
	var closeMutex sync.Mutex
	oneShot := &implementationRecord{
		mode:         subagent.ModeOneShot,
		startEntered: make(chan struct{}),
		startRelease: make(chan struct{}),
		closeOrder:   &closeOrder,
		closeMutex:   &closeMutex,
	}
	continuable := &continuableRecord{
		implementationRecord: &implementationRecord{
			mode:       subagent.ModeContinuable,
			closeOrder: &closeOrder,
			closeMutex: &closeMutex,
		},
	}
	owner := New()
	if err := owner.Open(
		agent.NewRegistry(agent.RegistryOptions{}),
		sharedexecution.NewRegistry(),
		oneShot,
		continuable,
	); err != nil {
		t.Fatal(err)
	}
	command, err := subagent.NewOneShotStart(
		subagent.ChildRequest{},
		subagent.OneShotOptions{
			SeedBuilder: "spawn",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	startDone := make(chan error, 1)
	go func() {
		_, startErr := owner.Start(context.Background(), command)
		startDone <- startErr
	}()
	<-oneShot.startEntered
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- owner.Close(context.Background())
	}()
	waitForAdmissionState(t, owner, admissionClosing)
	if _, startErr := owner.Start(context.Background(), command); startErr == nil {
		t.Fatal("Start was admitted while Service was closing")
	} else {
		var subagentErr *subagent.Error
		if !errors.As(startErr, &subagentErr) ||
			subagentErr.Code != subagent.ErrorDraining {
			t.Fatalf("Start error = %v", startErr)
		}
	}
	select {
	case closeErr := <-closeDone:
		t.Fatalf("Close returned before admitted Start: %v", closeErr)
	default:
	}
	close(oneShot.startRelease)
	if startErr := <-startDone; startErr != nil {
		t.Fatal(startErr)
	}
	if closeErr := <-closeDone; closeErr != nil {
		t.Fatal(closeErr)
	}
	closeMutex.Lock()
	actualOrder := append([]subagent.Mode(nil), closeOrder...)
	closeMutex.Unlock()
	wantOrder := []subagent.Mode{
		subagent.ModeContinuable,
		subagent.ModeOneShot,
	}
	if !reflect.DeepEqual(actualOrder, wantOrder) {
		t.Fatalf("close order = %v, want %v", actualOrder, wantOrder)
	}
}

func TestServiceOpenRejectsIncompleteImplementationSet(t *testing.T) {
	t.Parallel()
	agents := agent.NewRegistry(agent.RegistryOptions{})
	executions := sharedexecution.NewRegistry()
	testCases := []struct {
		name            string
		implementations []implementation
		message         string
	}{
		{
			name: "missing messaging and reporting",
			implementations: []implementation{
				&implementationRecord{
					mode: subagent.ModeOneShot,
				},
			},
			message: "must provide child messaging and parent reporting",
		},
		{
			name: "duplicate mode",
			implementations: []implementation{
				&continuableRecord{
					implementationRecord: &implementationRecord{
						mode: subagent.ModeContinuable,
					},
				},
				&continuableRecord{
					implementationRecord: &implementationRecord{
						mode: subagent.ModeContinuable,
					},
				},
			},
			message: "duplicate implementation",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			owner := New()
			err := owner.Open(
				agents,
				executions,
				testCase.implementations...,
			)
			if err == nil || !strings.Contains(err.Error(), testCase.message) {
				t.Fatalf("Open error = %v", err)
			}
		})
	}
}

func waitForAdmissionState(
	t *testing.T,
	owner *Service,
	want admissionState,
) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		owner.mutex.RLock()
		state := owner.state
		owner.mutex.RUnlock()
		if state == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("admission state = %v, want %v", state, want)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}
