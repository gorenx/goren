package agentloop

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHandleDisposalJoinsClosingTree(t *testing.T) {
	sentinel := errors.New("structural teardown failed")
	lifecycle := &agentLifecycle{
		closing: make(chan struct{}),
		closed:  make(chan struct{}),
	}
	if !lifecycle.beginClosing() {
		t.Fatal("first close did not acquire lifecycle teardown")
	}
	if lifecycle.beginClosing() {
		t.Fatal("second close acquired lifecycle teardown")
	}
	select {
	case <-lifecycle.ClosingSignal():
	default:
		t.Fatal("structural teardown did not notify the exact Handle owner")
	}
	joined := make(chan error, 1)
	go func() {
		joined <- lifecycle.Dispose(context.Background())
	}()
	select {
	case joinErr := <-joined:
		t.Fatalf("Dispose returned before structural teardown: %v", joinErr)
	case <-time.After(10 * time.Millisecond):
	}
	lifecycle.complete(sentinel)
	select {
	case joinErr := <-joined:
		if !errors.Is(joinErr, sentinel) {
			t.Fatalf("Dispose error = %v, want sentinel", joinErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Dispose did not join structural teardown")
	}
}
