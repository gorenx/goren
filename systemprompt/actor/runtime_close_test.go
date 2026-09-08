package promptactor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/systemprompt"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// delayedText models a provider that observes cancellation but still has cleanup to finish.
type delayedText struct {
	entered  chan<- struct{}
	canceled chan<- struct{}
	release  <-chan struct{}
}

func (source *delayedText) ResolveText(ctx context.Context, _ systemprompt.AssembleContext) (string, error) {
	source.entered <- struct{}{}
	<-ctx.Done()
	source.canceled <- struct{}{}
	<-source.release
	return "", ctx.Err()
}

type closeRequest struct {
	target *goakt.GrainIdentity
}

type closeRequester struct {
	replies chan<- promptactor.Completion
}

func (*closeRequester) PreStart(*goakt.Context) error { return nil }
func (*closeRequester) PostStop(*goakt.Context) error { return nil }
func (consumer *closeRequester) Receive(rc *goakt.ReceiveContext) {
	if incoming, ok := rc.Message().(closeRequest); ok {
		rc.RequestGrain(incoming.target, promptactor.CloseRuntime{}, goakt.WithRequestTimeout(4*time.Second)).Then(func(payload any, err error) {
			if err != nil {
				consumer.replies <- promptactor.Completion{
					Err: err,
				}
				return
			}
			consumer.replies <- payload.(promptactor.Completion)
		})
	}
}

func TestRuntimeCloseWaitsForProviderCleanup(t *testing.T) {
	_, system, _ := startRegistry(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	definitions := systemprompt.DeriveLayer(systemprompt.Snapshot{})
	if _, err := definitions.AddSection(ctx, systemprompt.PromptSection{
		Name: "cleanup",
		Text: &delayedText{
			entered:  entered,
			canceled: canceled,
			release:  release,
		},
	}); err != nil {
		t.Fatal(err)
	}
	identity, err := goakt.GrainOf[*promptactor.RuntimeGrain](ctx, system, "closing-agent", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := initializeRuntime(t, ctx, system, identity, "closing-agent", definitions.Capture(), nil)
	if err != nil || payload.(promptactor.RuntimeInitialized).Err != nil {
		t.Fatalf("initialize: %v, %v", payload, err)
	}
	payload, err = system.AskGrain(ctx, identity, promptactor.ApplyTools{
		AgentID: "closing-agent",
		Version: 1,
	}, time.Second)
	if err != nil || payload.(promptactor.Completion).Err != nil {
		t.Fatalf("tools: %v, %v", payload, err)
	}
	assembled := make(chan promptactor.Assembled, 1)
	worker, err := system.Spawn(ctx, "assembly-requester", &assemblyRequester{
		replies: assembled,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	if err := goakt.Tell(ctx, worker, assemblyRequest{
		Target: identity,
		Input: promptactor.Assemble{
			ID:           "active",
			ToolsVersion: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case reply := <-assembled:
		t.Fatalf("assembly failed before provider start: %+v", reply)
	case <-ctx.Done():
		t.Fatal("provider did not start")
	}
	closed := make(chan promptactor.Completion, 1)
	closer, err := system.Spawn(ctx, "close-requester", &closeRequester{
		replies: closed,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	if err := goakt.Tell(ctx, closer, closeRequest{
		target: identity,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("close did not cancel provider")
	}
	select {
	case reply := <-closed:
		t.Fatalf("close acknowledged before cleanup: %+v", reply)
	default:
	}
	payload, err = system.AskGrain(ctx, identity, promptactor.ApplyTools{
		AgentID: "closing-agent",
		Version: 2,
	}, time.Second)
	if err != nil || !errors.Is(payload.(promptactor.Completion).Err, promptactor.ErrUnavailable) {
		t.Fatalf("closing runtime accepted update: %v, %v", payload, err)
	}
	// Allow the provider to finish; cancellation alone must not release the owner.
	release <- struct{}{}
	select {
	case reply := <-assembled:
		if !errors.Is(reply.Err, context.Canceled) {
			t.Fatalf("assembly: %+v", reply)
		}
	case <-ctx.Done():
		t.Fatal("assembly did not finish")
	}
	select {
	case reply := <-closed:
		if reply.Err != nil {
			t.Fatal(reply.Err)
		}
	case <-ctx.Done():
		t.Fatal("close did not finish")
	}
	payload, err = system.AskGrain(ctx, identity, promptactor.CloseRuntime{}, time.Second)
	if err != nil || payload.(promptactor.Completion).Err != nil {
		t.Fatalf("repeat close: %v, %v", payload, err)
	}
	payload, err = system.AskGrain(ctx, identity, promptactor.Assemble{
		ID:           "after-close",
		ToolsVersion: 1,
	}, time.Second)
	if err != nil || !errors.Is(payload.(promptactor.Assembled).Err, promptactor.ErrUnavailable) {
		t.Fatalf("closed runtime accepted assembly: %v, %v", payload, err)
	}
}
