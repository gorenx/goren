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
	mutex       sync.Mutex
	resumeCalls int
	childID     session.SessionID
	message     llm.UserMessage
}

func (record *continuableRecord) Resume(
	_ context.Context,
	_ agent.Agent,
	childID session.SessionID,
	messageValue llm.UserMessage,
) (llm.MessageID, error) {
	record.mutex.Lock()
	record.resumeCalls++
	record.childID = childID
	record.message = messageValue
	record.mutex.Unlock()
	return messageValue.StableID(), nil
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

func TestServiceOpenRejectsDuplicateImplementationMode(t *testing.T) {
	t.Parallel()
	agents := agent.NewRegistry(agent.RegistryOptions{})
	executions := sharedexecution.NewRegistry()
	implementations := []implementation{
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
	}
	owner := New()
	err := owner.Open(
		agents,
		executions,
		implementations...,
	)
	if err == nil || !strings.Contains(err.Error(), "duplicate implementation") {
		t.Fatalf("Open error = %v", err)
	}
}

func TestServiceSendUsesResidentChildAgentForEveryMode(t *testing.T) {
	t.Parallel()
	for _, selectedMode := range []subagent.Mode{
		subagent.ModeOneShot,
		subagent.ModeContinuable,
	} {
		selectedMode := selectedMode
		t.Run(string(selectedMode), func(t *testing.T) {
			t.Parallel()
			parent := newServiceAgent(t, "parent", nil)
			parentID := parent.ID()
			child := newServiceAgent(
				t,
				session.SessionID("child-"+string(selectedMode)),
				&parentID,
			)
			agents := &agentRegistryRecord{
				entries: map[session.SessionID]agent.Agent{
					parent.ID(): parent,
					child.ID():  child,
				},
			}
			executions := sharedexecution.NewRegistry()
			running, executionErr := sharedexecution.New(
				subagent.RunID("run-"+string(selectedMode)),
				child.ID(),
				terminalRecord{},
			)
			if executionErr != nil {
				t.Fatal(executionErr)
			}
			if activationErr := running.Activate(); activationErr != nil {
				t.Fatal(activationErr)
			}
			closing := make(chan struct{})
			if publishErr := executions.Publish(sharedexecution.Entry{
				Execution: running,
				Mode:      selectedMode,
				Parent:    parent,
				Subject:   child,
				Closing:   closing,
			}); publishErr != nil {
				t.Fatal(publishErr)
			}
			owner := New()
			if openErr := owner.Open(
				agents,
				executions,
				&implementationRecord{
					mode: subagent.ModeOneShot,
				},
				&continuableRecord{
					implementationRecord: &implementationRecord{
						mode: subagent.ModeContinuable,
					},
				},
			); openErr != nil {
				t.Fatal(openErr)
			}
			messageID, sendErr := owner.Send(
				context.Background(),
				parent,
				child.ID(),
				[]llm.ContentBlock{
					llm.NewTextBlock("continue"),
				},
				subagent.FollowupOptions{
					Source: subagent.CoordinatorSource{
						SenderSessionID: parent.ID(),
					},
				},
			)
			if sendErr != nil {
				t.Fatal(sendErr)
			}
			child.mutex.Lock()
			followups := append([]llm.UserMessage(nil), child.followups...)
			child.mutex.Unlock()
			if len(followups) != 1 || followups[0].StableID() != messageID {
				t.Fatalf("resident child followups = %#v, messageID = %q", followups, messageID)
			}
		})
	}
}

func TestServiceSendUsesContinuableOnlyForColdResume(t *testing.T) {
	t.Parallel()
	parent := newServiceAgent(t, "parent", nil)
	agents := &agentRegistryRecord{
		entries: map[session.SessionID]agent.Agent{
			parent.ID(): parent,
		},
	}
	continuable := &continuableRecord{
		implementationRecord: &implementationRecord{
			mode: subagent.ModeContinuable,
		},
	}
	owner := New()
	if openErr := owner.Open(
		agents,
		sharedexecution.NewRegistry(),
		&implementationRecord{
			mode: subagent.ModeOneShot,
		},
		continuable,
	); openErr != nil {
		t.Fatal(openErr)
	}
	messageID, sendErr := owner.Send(
		context.Background(),
		parent,
		"cold-child",
		[]llm.ContentBlock{
			llm.NewTextBlock("resume"),
		},
		subagent.FollowupOptions{
			Source: subagent.CoordinatorSource{
				SenderSessionID: parent.ID(),
			},
		},
	)
	if sendErr != nil {
		t.Fatal(sendErr)
	}
	continuable.mutex.Lock()
	defer continuable.mutex.Unlock()
	if continuable.resumeCalls != 1 || continuable.childID != "cold-child" ||
		continuable.message.StableID() != messageID {
		t.Fatalf(
			"cold resume = calls:%d child:%q message:%q, returned:%q",
			continuable.resumeCalls,
			continuable.childID,
			continuable.message.StableID(),
			messageID,
		)
	}
}

func TestServiceSendReturnsResidentAgentErrorWithoutInterpretingExecutionState(
	t *testing.T,
) {
	t.Parallel()
	parent := newServiceAgent(t, "parent", nil)
	parentID := parent.ID()
	child := newServiceAgent(t, "stopping-child", &parentID)
	sentinel := errors.New("child Agent rejected message")
	child.followupErr = sentinel
	agents := &agentRegistryRecord{
		entries: map[session.SessionID]agent.Agent{
			parent.ID(): parent,
			child.ID():  child,
		},
	}
	termination := &blockingTerminal{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	running, executionErr := sharedexecution.New(
		"stopping-run",
		child.ID(),
		termination,
	)
	if executionErr != nil {
		t.Fatal(executionErr)
	}
	if activationErr := running.Activate(); activationErr != nil {
		t.Fatal(activationErr)
	}
	executions := sharedexecution.NewRegistry()
	if publishErr := executions.Publish(sharedexecution.Entry{
		Execution: running,
		Mode:      subagent.ModeOneShot,
		Parent:    parent,
		Subject:   child,
		Closing:   make(chan struct{}),
	}); publishErr != nil {
		t.Fatal(publishErr)
	}
	running.Stop(sharedexecution.StopNormal)
	<-termination.entered
	t.Cleanup(func() {
		close(termination.release)
	})
	owner := New()
	if openErr := owner.Open(
		agents,
		executions,
		&implementationRecord{
			mode: subagent.ModeOneShot,
		},
		&continuableRecord{
			implementationRecord: &implementationRecord{
				mode: subagent.ModeContinuable,
			},
		},
	); openErr != nil {
		t.Fatal(openErr)
	}
	_, sendErr := owner.Send(
		context.Background(),
		parent,
		child.ID(),
		[]llm.ContentBlock{
			llm.NewTextBlock("late message"),
		},
		subagent.FollowupOptions{
			Source: subagent.CoordinatorSource{
				SenderSessionID: parent.ID(),
			},
		},
	)
	if !errors.Is(sendErr, sentinel) {
		t.Fatalf("Send error = %v, want resident Agent error", sendErr)
	}
	child.mutex.Lock()
	followupCalls := child.followupCalls
	child.mutex.Unlock()
	if followupCalls != 1 {
		t.Fatalf("resident Agent Followup calls = %d, want 1", followupCalls)
	}
}

type agentRegistryRecord struct {
	entries map[session.SessionID]agent.Agent
}

func (record *agentRegistryRecord) Get(
	identifier session.SessionID,
) (agent.Agent, bool) {
	subject, found := record.entries[identifier]
	return subject, found
}

func (record *agentRegistryRecord) Contains(subject agent.Agent) bool {
	if subject == nil {
		return false
	}
	current, found := record.entries[subject.ID()]
	return found && agent.Same(current, subject)
}

func (record *agentRegistryRecord) List() []agent.Agent {
	result := make([]agent.Agent, 0, len(record.entries))
	for _, subject := range record.entries {
		result = append(result, subject)
	}
	return result
}

type serviceAgent struct {
	id            session.SessionID
	session       session.Context
	mutex         sync.Mutex
	followups     []llm.UserMessage
	followupCalls int
	followupErr   error
}

func newServiceAgent(
	t *testing.T,
	identifier session.SessionID,
	parentID *session.SessionID,
) *serviceAgent {
	t.Helper()
	conversation, sessionErr := session.New(
		identifier,
		session.CreateOptions{
			Metadata: session.Metadata{
				ParentSession: parentID,
			},
		},
	)
	if sessionErr != nil {
		t.Fatal(sessionErr)
	}
	return &serviceAgent{
		id:      identifier,
		session: conversation,
	}
}

func (subject *serviceAgent) ID() session.SessionID { return subject.id }
func (*serviceAgent) OptionsValue() agent.Options   { return agent.Options{} }
func (subject *serviceAgent) SessionValue() session.Context {
	return subject.session
}
func (*serviceAgent) InboxValue() *agent.Inbox                      { return nil }
func (*serviceAgent) StatusValue() agent.Status                     { return agent.StatusIdle }
func (*serviceAgent) Cancel(agent.CancelCause, agent.CancelOptions) {}
func (*serviceAgent) WhenIdle(context.Context) error                { return nil }
func (*serviceAgent) RunMaintenance(context.Context, func(context.Context) error) error {
	return nil
}
func (*serviceAgent) Send(llm.UserMessage, agent.InboxTarget, bool) error {
	return nil
}
func (subject *serviceAgent) Followup(messageValue llm.UserMessage) error {
	subject.mutex.Lock()
	subject.followupCalls++
	subject.followups = append(subject.followups, messageValue)
	followupErr := subject.followupErr
	subject.mutex.Unlock()
	return followupErr
}
func (*serviceAgent) Steer(llm.UserMessage) error  { return nil }
func (*serviceAgent) Inject(llm.UserMessage) error { return nil }

type terminalRecord struct{}

func (terminalRecord) Terminate(
	context.Context,
	sharedexecution.StopCause,
) (subagent.Terminal, error) {
	return subagent.Terminal{}, nil
}

type blockingTerminal struct {
	entered chan struct{}
	release chan struct{}
}

func (terminal *blockingTerminal) Terminate(
	context.Context,
	sharedexecution.StopCause,
) (subagent.Terminal, error) {
	close(terminal.entered)
	<-terminal.release
	return subagent.Terminal{}, nil
}

var _ agent.Registry = (*agentRegistryRecord)(nil)
var _ agent.Agent = (*serviceAgent)(nil)
var _ sharedexecution.Terminator = terminalRecord{}
var _ sharedexecution.Terminator = (*blockingTerminal)(nil)

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
