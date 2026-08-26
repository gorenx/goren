package childdirectory

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	sessionpersistence "github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/subagent"
)

type cancellationPersistence struct {
	mutex        sync.Mutex
	header       session.Header
	listCalls    int
	inspectCalls int
	blockList    bool
	callsChanged chan struct{}
}

func (source *cancellationPersistence) List(
	requestContext context.Context,
) ([]session.Header, error) {
	source.mutex.Lock()
	source.listCalls++
	block := source.blockList
	source.notifyLocked()
	source.mutex.Unlock()
	if block {
		<-requestContext.Done()
		return nil, context.Cause(requestContext)
	}
	return []session.Header{source.header}, nil
}

func (source *cancellationPersistence) Inspect(
	requestContext context.Context,
	_ session.SessionID,
) (sessionpersistence.Inspection, error) {
	source.mutex.Lock()
	source.inspectCalls++
	source.notifyLocked()
	source.mutex.Unlock()
	<-requestContext.Done()
	return sessionpersistence.Inspection{}, context.Cause(requestContext)
}

func (source *cancellationPersistence) notifyLocked() {
	if source.callsChanged == nil {
		return
	}
	select {
	case source.callsChanged <- struct{}{}:
	default:
	}
}

func (source *cancellationPersistence) calls() (int, int) {
	source.mutex.Lock()
	defer source.mutex.Unlock()
	return source.listCalls, source.inspectCalls
}

func TestPreCancelledListingPerformsNoPersistenceRead(t *testing.T) {
	durability := &cancellationPersistence{}
	directory := New()
	if enableErr := directory.Enable(
		sessionList{},
		durability,
		projectionRegistry(t),
	); enableErr != nil {
		t.Fatal(enableErr)
	}
	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	_, listErr := directory.ListChildren(requestContext, "root")
	assertSubagentCode(t, listErr, subagent.ErrorCancelled)
	listCalls, inspectCalls := durability.calls()
	if listCalls != 0 || inspectCalls != 0 {
		t.Fatalf("persistence calls = list %d, inspect %d", listCalls, inspectCalls)
	}
}

func TestCancellationIsForwardedToPersistedListing(t *testing.T) {
	durability := &cancellationPersistence{
		blockList:    true,
		callsChanged: make(chan struct{}, 1),
	}
	directory := New()
	if enableErr := directory.Enable(
		sessionList{},
		durability,
		projectionRegistry(t),
	); enableErr != nil {
		t.Fatal(enableErr)
	}
	requestContext, cancelRequest := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, listErr := directory.ListChildren(requestContext, "root")
		result <- listErr
	}()
	waitForDirectoryCalls(t, durability, 1, 0)
	cancelRequest()
	assertSubagentCode(t, <-result, subagent.ErrorCancelled)
}

func TestCancellationIsForwardedToColdInspection(t *testing.T) {
	child := newDirectorySession(
		t,
		"cold-child",
		"root",
		1,
		subagent.ContinuableDescriptor{
			Provider: "spawn",
			Label:    "child",
		},
	)
	durability := &cancellationPersistence{
		header:       child.Header(),
		callsChanged: make(chan struct{}, 1),
	}
	directory := New()
	if enableErr := directory.Enable(
		sessionList{},
		durability,
		projectionRegistry(t),
	); enableErr != nil {
		t.Fatal(enableErr)
	}
	requestContext, cancelRequest := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, listErr := directory.ListChildren(requestContext, "root")
		result <- listErr
	}()
	waitForDirectoryCalls(t, durability, 1, 1)
	cancelRequest()
	assertSubagentCode(t, <-result, subagent.ErrorCancelled)
}

func waitForDirectoryCalls(
	t *testing.T,
	durability *cancellationPersistence,
	wantList int,
	wantInspect int,
) {
	t.Helper()
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	for {
		listCalls, inspectCalls := durability.calls()
		if listCalls >= wantList && inspectCalls >= wantInspect {
			return
		}
		select {
		case <-durability.callsChanged:
		case <-timeout.C:
			t.Fatal("catalog persistence call was not observed")
		}
	}
}
