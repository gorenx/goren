package promptactor_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/systemprompt"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func startRegistry(t *testing.T) (*promptactor.Client, goakt.ActorSystem, *goakt.GrainIdentity) {
	t.Helper()
	system, err := goakt.NewActorSystem("prompt-test", goakt.WithLogger(log.DiscardLogger), goakt.WithPubSub())
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := system.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	settings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		t.Fatal(err)
	}
	business, err := systemprompt.NewLayer(settings)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := goakt.GrainOf[*promptactor.Grain](context.Background(), system, promptactor.Name, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := system.AskGrain(context.Background(), identity, promptactor.Initialize{
		Layer: business,
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	reply, ok := payload.(promptactor.Completion)
	if !ok || reply.Err != nil {
		t.Fatal("prompt initialization failed")
	}
	endpoint, err := promptactor.NewClient(system, promptactor.Name)
	if err != nil {
		t.Fatal(err)
	}
	return endpoint, system, identity
}

func TestRegistrationIdentityAndCapturedMembership(t *testing.T) {
	endpoint, _, _ := startRegistry(t)
	ctx := context.Background()
	original, err := endpoint.AddSection(ctx, systemprompt.PromptSection{
		Name: "rule",
		Text: systemprompt.StaticText("first rule"),
	})
	if err != nil {
		t.Fatal(err)
	}
	retained, err := endpoint.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := endpoint.AddSection(ctx, systemprompt.PromptSection{
		Name: "rule",
		Text: systemprompt.StaticText("duplicate"),
	}); err == nil {
		t.Fatal("duplicate registration succeeded")
	}
	unchanged, err := endpoint.Prepare(ctx)
	if err != nil || unchanged.Version != retained.Version {
		t.Fatal("failed registration published a version")
	}
	if _, err := endpoint.Remove(ctx, original); err != nil {
		t.Fatal(err)
	}
	if _, err := endpoint.AddSection(ctx, systemprompt.PromptSection{
		Name: "rule",
		Text: systemprompt.StaticText("second rule"),
	}); err != nil {
		t.Fatal(err)
	}
	beforeRemoval, err := endpoint.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := endpoint.Remove(ctx, original); removed || err != nil {
		t.Fatalf("stale removal = %v, %v", removed, err)
	}
	afterRemoval, err := endpoint.Prepare(ctx)
	if err != nil || afterRemoval.Version != beforeRemoval.Version {
		t.Fatal("stale removal published a version")
	}
	earlier, err := retained.Snapshot.Assemble(ctx, systemprompt.AssembleContext{})
	if err != nil {
		t.Fatal(err)
	}
	oldText, err := systemprompt.RenderPrompt(earlier)
	if err != nil || !strings.Contains(oldText, "first rule") {
		t.Fatal("old snapshot changed after registry update")
	}
	present, err := endpoint.Assemble(ctx, systemprompt.AssembleContext{})
	if err != nil {
		t.Fatal(err)
	}
	newText, err := systemprompt.RenderPrompt(present)
	if err != nil || !strings.Contains(newText, "second rule") {
		t.Fatal("stale registration removed its replacement")
	}
}

func TestProviderCanRegisterWithoutBlockingRegistryMailbox(t *testing.T) {
	endpoint, _, _ := startRegistry(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := endpoint.AddSection(ctx, systemprompt.PromptSection{
		Name: "dynamic",
		Text: &registeringText{
			endpoint: endpoint,
		},
	}); err != nil {
		t.Fatal(err)
	}
	before, err := endpoint.Assemble(ctx, systemprompt.AssembleContext{})
	if err != nil {
		t.Fatal(err)
	}
	beforeText, err := systemprompt.RenderPrompt(before)
	if err != nil || strings.Contains(beforeText, "late contribution") {
		t.Fatal("membership changed during evaluation")
	}
	after, err := endpoint.Assemble(ctx, systemprompt.AssembleContext{})
	if err != nil {
		t.Fatal(err)
	}
	afterText, err := systemprompt.RenderPrompt(after)
	if err != nil || !strings.Contains(afterText, "late contribution") {
		t.Fatal("next request did not see registration")
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if _, err := endpoint.Prepare(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

type captureRequest struct {
	Target *goakt.GrainIdentity
}

type requester struct {
	replies chan promptactor.Prepared
}

func (*requester) PreStart(*goakt.Context) error { return nil }
func (*requester) PostStop(*goakt.Context) error { return nil }
func (consumer *requester) Receive(rc *goakt.ReceiveContext) {
	if incoming, ok := rc.Message().(captureRequest); ok {
		rc.RequestGrain(incoming.Target, promptactor.Prepare{}, goakt.WithRequestTimeout(time.Second)).Then(func(payload any, err error) {
			if err != nil {
				consumer.replies <- promptactor.Prepared{
					Err: err,
				}
				return
			}
			reply, ok := payload.(promptactor.Prepared)
			if !ok {
				reply.Err = errors.New("test: unexpected snapshot response")
			}
			consumer.replies <- reply
		})
	}
}

func TestActorReceivesSnapshotUsingAsyncRequest(t *testing.T) {
	_, system, identity := startRegistry(t)
	replies := make(chan promptactor.Prepared, 1)
	pid, err := system.Spawn(context.Background(), "consumer", &requester{
		replies: replies,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	if err := goakt.Tell(context.Background(), pid, captureRequest{
		Target: identity,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case reply := <-replies:
		if reply.Err != nil || reply.Version != 1 {
			t.Fatalf("snapshot request failed: %v", reply.Err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("snapshot response timed out")
	}
}

type registeringText struct {
	endpoint   *promptactor.Client
	registered bool
}

func (source *registeringText) ResolveText(ctx context.Context, _ systemprompt.AssembleContext) (string, error) {
	if !source.registered {
		if _, err := source.endpoint.AddSection(ctx, systemprompt.PromptSection{
			Name: "late",
			Text: systemprompt.StaticText("late contribution"),
		}); err != nil {
			return "", err
		}
		source.registered = true
	}
	return "dynamic contribution", nil
}
