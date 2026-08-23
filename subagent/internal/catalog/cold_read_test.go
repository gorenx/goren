package catalog

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	sessionpersistence "github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/subagent"
)

type gatedPersistence struct {
	mutex       sync.Mutex
	headers     []session.Header
	inspections map[session.SessionID]sessionpersistence.Inspection
	gate        <-chan struct{}
	entered     chan session.SessionID
	active      int
	peak        int
}

func (source *gatedPersistence) List(
	context.Context,
) ([]session.Header, error) {
	return append([]session.Header(nil), source.headers...), nil
}

func (source *gatedPersistence) Inspect(
	requestContext context.Context,
	identifier session.SessionID,
) (sessionpersistence.Inspection, error) {
	source.mutex.Lock()
	source.active++
	if source.active > source.peak {
		source.peak = source.active
	}
	source.mutex.Unlock()
	select {
	case source.entered <- identifier:
	case <-requestContext.Done():
		source.leave()
		return sessionpersistence.Inspection{}, context.Cause(requestContext)
	}
	select {
	case <-source.gate:
	case <-requestContext.Done():
		source.leave()
		return sessionpersistence.Inspection{}, context.Cause(requestContext)
	}
	source.leave()
	inspection, found := source.inspections[identifier]
	if !found {
		return sessionpersistence.Inspection{}, errors.New("missing inspection")
	}
	return inspection, nil
}

func (source *gatedPersistence) leave() {
	source.mutex.Lock()
	source.active--
	source.mutex.Unlock()
}

func (source *gatedPersistence) peakReads() int {
	source.mutex.Lock()
	defer source.mutex.Unlock()
	return source.peak
}

func TestColdReadsUseBoundedConcurrency(t *testing.T) {
	rootID := session.SessionID("bounded-root")
	headers := make([]session.Header, 0, 8)
	inspections := make(map[session.SessionID]sessionpersistence.Inspection, 8)
	for index := range 8 {
		identifier := session.SessionID("cold-" + string(rune('a'+index)))
		conversation := newCatalogSession(
			t,
			identifier,
			rootID,
			int64(index),
			subagent.ContinuableDescriptor{
				Provider: "spawn",
				Label:    string(identifier),
			},
		)
		headers = append(headers, conversation.Header())
		inspections[identifier] = inspectionOf(conversation)
	}
	gate := make(chan struct{})
	durability := &gatedPersistence{
		headers:     headers,
		inspections: inspections,
		gate:        gate,
		entered:     make(chan session.SessionID, len(headers)),
	}
	catalogService := New()
	if enableErr := catalogService.Enable(
		sessionList{},
		durability,
		projectionRegistry(t),
	); enableErr != nil {
		t.Fatal(enableErr)
	}
	result := make(chan error, 1)
	go func() {
		entries, listErr := catalogService.ListChildren(context.Background(), rootID)
		if listErr == nil && len(entries) != len(headers) {
			listErr = errors.New("catalog returned the wrong entry count")
		}
		result <- listErr
	}()
	for range coldReadConcurrency {
		select {
		case <-durability.entered:
		case <-time.After(time.Second):
			close(gate)
			t.Fatal("cold read worker did not enter")
		}
	}
	if peak := durability.peakReads(); peak != coldReadConcurrency {
		close(gate)
		t.Fatalf("peak cold reads = %d, want %d", peak, coldReadConcurrency)
	}
	select {
	case identifier := <-durability.entered:
		close(gate)
		t.Fatalf("unbounded cold read entered for %q", identifier)
	default:
	}
	close(gate)
	select {
	case listErr := <-result:
		if listErr != nil {
			t.Fatal(listErr)
		}
	case <-time.After(time.Second):
		t.Fatal("catalog did not finish after releasing cold reads")
	}
}
