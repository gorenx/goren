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
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	boundcontract "github.com/gorenx/goren/subagent/bound"
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

type oneShotRecord struct {
	*implementationRecord
}

func (record *oneShotRecord) Start(
	_ context.Context,
	_ subagent.OneShotStartCommand,
) (subagent.Execution, error) {
	if record.startEntered != nil {
		close(record.startEntered)
	}
	if record.startRelease != nil {
		<-record.startRelease
	}
	return nil, nil
}

type continuableRecord struct {
	*implementationRecord
	mutex          sync.Mutex
	resumeCalls    int
	childSessionID session.SessionID
	message        agentmessage.UserMessage
}

func (*continuableRecord) Start(
	context.Context,
	subagent.ContinuableStartCommand,
) (subagent.Execution, error) {
	return nil, nil
}

type boundMessageSpy struct {
	mutex          sync.Mutex
	calls          int
	childSessionID session.SessionID
	message        agentmessage.UserMessage
}

// boundImplementationStub satisfies the complete Bound port so Service tests
// can isolate cross-mode routing. Bound behavior itself is tested by its owner.
type boundImplementationStub struct {
	*implementationRecord
	// Key is a child Session ID. Value reports whether the owning user Session
	// has a durable Bound Binding for it.
	bindings map[session.SessionID]bool
	messages *boundMessageSpy
}

func (*boundImplementationStub) List(
	context.Context,
) ([]boundcontract.Definition, error) {
	return nil, nil
}

func (*boundImplementationStub) Create(
	_ context.Context,
	creation boundcontract.Creation,
) (boundcontract.Definition, error) {
	return boundcontract.NewDefinition(creation.Definition, 1)
}

func (*boundImplementationStub) Replace(
	_ context.Context,
	replacement boundcontract.Replacement,
) (boundcontract.Definition, error) {
	return boundcontract.NewDefinition(
		replacement.Definition,
		replacement.ExpectedRevision+1,
	)
}

func (*boundImplementationStub) SessionStarted(agent.Agent) {}

func (*boundImplementationStub) Deliver(
	_ context.Context,
	_ boundcontract.Address,
	inputValue boundcontract.Input,
) (boundcontract.Receipt, error) {
	return boundcontract.Receipt{
		InputID:   inputValue.ID,
		MessageID: "bound-input",
	}, nil
}

func (*boundImplementationStub) AgentDisposed(context.Context, agent.Agent) error {
	return nil
}

func (stub *boundImplementationStub) HasBinding(
	_ context.Context,
	_ agent.Agent,
	childSessionID session.SessionID,
) (bool, error) {
	return stub.bindings[childSessionID], nil
}

func (stub *boundImplementationStub) Followup(
	_ context.Context,
	_ agent.Agent,
	childSessionID session.SessionID,
	messageValue agentmessage.UserMessage,
) (agentmessage.MessageID, error) {
	if stub.messages != nil {
		stub.messages.mutex.Lock()
		stub.messages.calls++
		stub.messages.childSessionID = childSessionID
		stub.messages.message = messageValue
		stub.messages.mutex.Unlock()
	}
	return messageValue.StableID(), nil
}

func (record *continuableRecord) Resume(
	_ context.Context,
	_ agent.Agent,
	childSessionID session.SessionID,
	messageValue agentmessage.UserMessage,
) (agentmessage.MessageID, error) {
	record.mutex.Lock()
	record.resumeCalls++
	record.childSessionID = childSessionID
	record.message = messageValue
	record.mutex.Unlock()
	return messageValue.StableID(), nil
}

func TestServiceCloseStopsAdmissionAndWaitsForAdmittedStart(t *testing.T) {
	t.Parallel()
	closeOrder := make([]subagent.Mode, 0, 2)
	var closeMutex sync.Mutex
	oneShotMode := &oneShotRecord{
		implementationRecord: &implementationRecord{
			mode:         subagent.ModeOneShot,
			startEntered: make(chan struct{}),
			startRelease: make(chan struct{}),
			closeOrder:   &closeOrder,
			closeMutex:   &closeMutex,
		},
	}
	continuableMode := &continuableRecord{
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
		oneShotMode,
		continuableMode,
	); err != nil {
		t.Fatal(err)
	}
	command, err := subagent.NewOneShotStart(
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
	<-oneShotMode.startEntered
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
	close(oneShotMode.startRelease)
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

func TestServiceOpenRejectsIncompleteModeContract(t *testing.T) {
	t.Parallel()
	owner := New()
	openErr := owner.Open(
		agent.NewRegistry(agent.RegistryOptions{}),
		sharedexecution.NewRegistry(),
		&implementationRecord{
			mode: subagent.ModeContinuable,
		},
	)
	if openErr == nil || !strings.Contains(openErr.Error(), "incomplete") {
		t.Fatalf("Open error = %v", openErr)
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
			running := newManagedExecutionStub(
				subagent.RunID("run-"+string(selectedMode)),
				child.ID(),
				nil,
				nil,
			)
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
				&oneShotRecord{
					implementationRecord: &implementationRecord{
						mode: subagent.ModeOneShot,
					},
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
				[]agentmessage.ContentBlock{
					agentmessage.NewTextBlock("continue"),
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
			followups := append([]agentmessage.UserMessage(nil), child.followups...)
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
	continuableMode := &continuableRecord{
		implementationRecord: &implementationRecord{
			mode: subagent.ModeContinuable,
		},
	}
	owner := New()
	if openErr := owner.Open(
		agents,
		sharedexecution.NewRegistry(),
		&oneShotRecord{
			implementationRecord: &implementationRecord{
				mode: subagent.ModeOneShot,
			},
		},
		continuableMode,
	); openErr != nil {
		t.Fatal(openErr)
	}
	messageID, sendErr := owner.Send(
		context.Background(),
		parent,
		"cold-child",
		[]agentmessage.ContentBlock{
			agentmessage.NewTextBlock("resume"),
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
	continuableMode.mutex.Lock()
	defer continuableMode.mutex.Unlock()
	if continuableMode.resumeCalls != 1 || continuableMode.childSessionID != "cold-child" ||
		continuableMode.message.StableID() != messageID {
		t.Fatalf(
			"cold resume = calls:%d child:%q message:%q, returned:%q",
			continuableMode.resumeCalls,
			continuableMode.childSessionID,
			continuableMode.message.StableID(),
			messageID,
		)
	}
}

func TestServiceSendUsesBoundOwnerForColdBinding(t *testing.T) {
	t.Parallel()
	parent := newServiceAgent(t, "parent", nil)
	agents := &agentRegistryRecord{
		entries: map[session.SessionID]agent.Agent{
			parent.ID(): parent,
		},
	}
	continuableMode := &continuableRecord{
		implementationRecord: &implementationRecord{
			mode: subagent.ModeContinuable,
		},
	}
	boundMessages := &boundMessageSpy{}
	boundMode := &boundImplementationStub{
		implementationRecord: &implementationRecord{
			mode: subagent.ModeBound,
		},
		bindings: map[session.SessionID]bool{
			"cold-bound": true,
		},
		messages: boundMessages,
	}
	owner := New()
	if err := owner.Open(
		agents,
		sharedexecution.NewRegistry(),
		continuableMode,
		boundMode,
	); err != nil {
		t.Fatal(err)
	}
	messageID, err := owner.Send(
		context.Background(),
		parent,
		"cold-bound",
		[]agentmessage.ContentBlock{
			agentmessage.NewTextBlock("bound work"),
		},
		subagent.FollowupOptions{
			Source: subagent.CoordinatorSource{
				SenderSessionID: parent.ID(),
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	boundMessages.mutex.Lock()
	boundCalls := boundMessages.calls
	boundChildID := boundMessages.childSessionID
	boundMessageID := boundMessages.message.StableID()
	boundMessages.mutex.Unlock()
	continuableMode.mutex.Lock()
	continuableCalls := continuableMode.resumeCalls
	continuableMode.mutex.Unlock()
	if boundCalls != 1 || boundChildID != "cold-bound" ||
		boundMessageID != messageID || continuableCalls != 0 {
		t.Fatalf(
			"cold Bound route = bound calls:%d child:%q message:%q, continuable calls:%d, returned:%q",
			boundCalls,
			boundChildID,
			boundMessageID,
			continuableCalls,
			messageID,
		)
	}
}

func TestServiceSendUsesBoundOwnerForResidentChild(t *testing.T) {
	t.Parallel()
	parent := newServiceAgent(t, "parent", nil)
	parentID := parent.ID()
	child := newServiceAgent(t, "resident-bound", &parentID)
	agents := &agentRegistryRecord{
		entries: map[session.SessionID]agent.Agent{
			parent.ID(): parent,
			child.ID():  child,
		},
	}
	executions := sharedexecution.NewRegistry()
	running := newManagedExecutionStub(
		"bound-run",
		child.ID(),
		nil,
		nil,
	)
	if err := running.Activate(); err != nil {
		t.Fatal(err)
	}
	if err := executions.Publish(
		sharedexecution.Entry{
			Execution: running,
			Mode:      subagent.ModeBound,
			Parent:    parent,
			Subject:   child,
			Closing:   make(chan struct{}),
		},
	); err != nil {
		t.Fatal(err)
	}
	boundMessages := &boundMessageSpy{}
	boundMode := &boundImplementationStub{
		implementationRecord: &implementationRecord{
			mode: subagent.ModeBound,
		},
		messages: boundMessages,
	}
	owner := New()
	if err := owner.Open(agents, executions, boundMode); err != nil {
		t.Fatal(err)
	}
	messageID, err := owner.Send(
		context.Background(),
		parent,
		child.ID(),
		[]agentmessage.ContentBlock{
			agentmessage.NewTextBlock("resident bound work"),
		},
		subagent.FollowupOptions{
			Source: subagent.CoordinatorSource{
				SenderSessionID: parent.ID(),
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	boundMessages.mutex.Lock()
	boundCalls := boundMessages.calls
	boundMessageID := boundMessages.message.StableID()
	boundMessages.mutex.Unlock()
	child.mutex.Lock()
	directFollowups := child.followupCalls
	child.mutex.Unlock()
	if boundCalls != 1 || boundMessageID != messageID || directFollowups != 0 {
		t.Fatalf(
			"resident Bound route = bound calls:%d message:%q, direct followups:%d, returned:%q",
			boundCalls,
			boundMessageID,
			directFollowups,
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
	entered := make(chan struct{})
	release := make(chan struct{})
	running := newManagedExecutionStub(
		"stopping-run",
		child.ID(),
		entered,
		release,
	)
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
	running.Stop(sharedexecution.CloseNormal)
	<-entered
	t.Cleanup(func() {
		close(release)
	})
	owner := New()
	if openErr := owner.Open(
		agents,
		executions,
		&oneShotRecord{
			implementationRecord: &implementationRecord{
				mode: subagent.ModeOneShot,
			},
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
		[]agentmessage.ContentBlock{
			agentmessage.NewTextBlock("late message"),
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
	// Key is a Session ID. Value is the exact live Agent epoch registered for it.
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
	response := make([]agent.Agent, 0, len(record.entries))
	for _, subject := range record.entries {
		response = append(response, subject)
	}
	return response
}

type serviceAgent struct {
	id            session.SessionID
	session       session.Context
	mutex         sync.Mutex
	followups     []agentmessage.UserMessage
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
func (*serviceAgent) Send(agentmessage.UserMessage, agent.InboxTarget, bool) error {
	return nil
}
func (subject *serviceAgent) Followup(messageValue agentmessage.UserMessage) error {
	subject.mutex.Lock()
	subject.followupCalls++
	subject.followups = append(subject.followups, messageValue)
	followupErr := subject.followupErr
	subject.mutex.Unlock()
	return followupErr
}
func (*serviceAgent) Steer(agentmessage.UserMessage) error  { return nil }
func (*serviceAgent) Inject(agentmessage.UserMessage) error { return nil }

type managedExecutionStub struct {
	mutex          sync.RWMutex
	executionRunID subagent.RunID
	childSessionID session.SessionID
	phase          subagent.ExecutionState
	done           chan struct{}
	entered        chan struct{}
	release        chan struct{}
}

func newManagedExecutionStub(
	executionRunID subagent.RunID,
	childSessionID session.SessionID,
	entered chan struct{},
	release chan struct{},
) *managedExecutionStub {
	return &managedExecutionStub{
		executionRunID: executionRunID,
		childSessionID: childSessionID,
		phase:          subagent.ExecutionStarting,
		done:           make(chan struct{}),
		entered:        entered,
		release:        release,
	}
}

func (running *managedExecutionStub) Activate() error {
	running.mutex.Lock()
	running.phase = subagent.ExecutionActive
	running.mutex.Unlock()
	return nil
}

func (running *managedExecutionStub) Stop(sharedexecution.CloseCause) {
	running.mutex.Lock()
	if running.phase == subagent.ExecutionStopping ||
		running.phase == subagent.ExecutionStopped {
		running.mutex.Unlock()
		return
	}
	running.phase = subagent.ExecutionStopping
	running.mutex.Unlock()
	go func() {
		if running.entered != nil {
			close(running.entered)
		}
		if running.release != nil {
			<-running.release
		}
		running.mutex.Lock()
		running.phase = subagent.ExecutionStopped
		close(running.done)
		running.mutex.Unlock()
	}()
}

func (running *managedExecutionStub) StopAndWait(
	closeContext context.Context,
	cause sharedexecution.CloseCause,
) error {
	running.Stop(cause)
	return running.Wait(closeContext)
}

func (running *managedExecutionStub) RunID() subagent.RunID {
	return running.executionRunID
}

func (running *managedExecutionStub) ChildID() session.SessionID {
	return running.childSessionID
}

func (running *managedExecutionStub) State() subagent.ExecutionState {
	running.mutex.RLock()
	stateValue := running.phase
	running.mutex.RUnlock()
	return stateValue
}

func (running *managedExecutionStub) Wait(waitContext context.Context) error {
	select {
	case <-running.done:
		return nil
	case <-waitContext.Done():
		return context.Cause(waitContext)
	}
}

func (*managedExecutionStub) Result() (subagent.Terminal, bool) {
	return subagent.Terminal{}, false
}

func (running *managedExecutionStub) Dispose(closeContext context.Context) error {
	return running.StopAndWait(closeContext, sharedexecution.CloseDisposed)
}

var _ agent.Registry = (*agentRegistryRecord)(nil)
var _ agent.Agent = (*serviceAgent)(nil)
var _ sharedexecution.ManagedExecution = (*managedExecutionStub)(nil)

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
		phase := owner.phase
		owner.mutex.RUnlock()
		if phase == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("admission state = %v, want %v", phase, want)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}
