package plugin

import (
	"context"
	"testing"
	"time"
)

func TestSubagentEventPublisherStartsAReplacementWorker(t *testing.T) {
	publisher := newSubagentEventPublisher()
	for _, name := range []string{"first", "second"} {
		publisherRun, err := publisher.start()
		if err != nil {
			t.Fatal(err)
		}
		observed := make(chan string, 1)
		go func() {
			defer close(publisherRun.done)
			for {
				select {
				case requested := <-publisher.requests:
					removed, matches := requested.(seedBuilderRemovedPublication)
					if !matches {
						t.Errorf("event request type = %T", requested)
						return
					}
					observed <- removed.name
					close(removed.done)
				case <-publisherRun.stopped:
					return
				}
			}
		}()
		publisher.PublishRemoved(context.Background(), name)
		if received := <-observed; received != name {
			t.Fatalf("removed name = %q, want %q", received, name)
		}
		if stopped := publisher.stop(); stopped != publisherRun {
			t.Fatal("event publisher stopped a different worker")
		}
		<-publisherRun.done
	}
}

func TestPluginDisposeBeforeApplyDoesNotWaitForEventPublisher(t *testing.T) {
	owner := New(Diagnostics{}, DefinitionDatabase{})
	disposed := make(chan error, 1)
	go func() {
		disposed <- owner.Dispose(context.Background())
	}()
	select {
	case err := <-disposed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Dispose waited for an event publisher that was never started")
	}
}
