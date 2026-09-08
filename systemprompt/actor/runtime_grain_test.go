package promptactor_test

import (
	"context"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/systemprompt"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func TestRuntimeGrainAppliesEventsWhileAssemblyIsRunning(t *testing.T) {
	_, system, _ := startRegistry(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	settings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := systemprompt.NewLayer(settings)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	if _, err := definitions.AddSection(ctx, systemprompt.PromptSection{
		Name: "blocking",
		Text: &blockingText{
			entered: entered,
			release: release,
		},
	}); err != nil {
		t.Fatal(err)
	}
	identity, err := goakt.GrainOf[*promptactor.RuntimeGrain](ctx, system, "agent-runtime", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := initializeRuntime(t, ctx, system, identity, "agent-runtime", definitions.Capture(), nil)
	if err != nil || payload.(promptactor.RuntimeInitialized).Err != nil {
		t.Fatalf("initialize: %v, %v", payload, err)
	}
	payload, err = system.AskGrain(ctx, identity, promptactor.ApplyTools{
		AgentID:     "agent-runtime",
		Version:     1,
		Definitions: systemprompt.ToolDefinitions{},
	}, time.Second)
	if err != nil || payload.(promptactor.Completion).Err != nil {
		t.Fatalf("initial tools: %v, %v", payload, err)
	}
	completed := make(chan promptactor.Assembled, 2)
	consumer, err := system.Spawn(ctx, "assembly-consumer", &assemblyRequester{
		replies: completed,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	if err := goakt.Tell(ctx, consumer, assemblyRequest{
		Target: identity,
		Input: promptactor.Assemble{
			ID:           "first",
			ToolsVersion: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("assembly did not start")
	}
	payload, err = system.AskGrain(ctx, identity, promptactor.ApplyTools{
		AgentID: "agent-runtime",
		Version: 2,
		Definitions: systemprompt.ToolDefinitions{
			Schemas: []llm.ToolSchema{
				{
					Name:       "read",
					Parameters: []byte(`{"type":"object"}`),
				},
			},
		},
	}, time.Second)
	if err != nil || payload.(promptactor.Completion).Err != nil {
		t.Fatalf("mailbox blocked by assembly: %v, %v", payload, err)
	}
	close(release)
	select {
	case reply := <-completed:
		if reply.Err != nil || len(reply.Prompt.Tools) != 0 {
			t.Fatalf("active assembly changed: %#v", reply)
		}
	case <-ctx.Done():
		t.Fatal("assembly did not complete")
	}
	if err := goakt.Tell(ctx, consumer, assemblyRequest{
		Target: identity,
		Input: promptactor.Assemble{
			ID:           "second",
			ToolsVersion: 2,
		},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case reply := <-completed:
		if reply.Err != nil || len(reply.Prompt.Tools) != 1 {
			t.Fatalf("next assembly missing update: %#v", reply)
		}
	case <-ctx.Done():
		t.Fatal("second assembly did not complete")
	}

}

type blockingText struct {
	entered chan<- struct{}
	release <-chan struct{}
}

func (source *blockingText) ResolveText(ctx context.Context, _ systemprompt.AssembleContext) (string, error) {
	select {
	case source.entered <- struct{}{}:
	default:
	}
	select {
	case <-source.release:
		return "ready", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type assemblyRequest struct {
	Target *goakt.GrainIdentity
	Input  promptactor.Assemble
}

type assemblyRequester struct {
	replies chan<- promptactor.Assembled
}

func (*assemblyRequester) PreStart(*goakt.Context) error { return nil }
func (*assemblyRequester) PostStop(*goakt.Context) error { return nil }
func (consumer *assemblyRequester) Receive(rc *goakt.ReceiveContext) {
	if incoming, ok := rc.Message().(assemblyRequest); ok {
		rc.RequestGrain(incoming.Target, incoming.Input, goakt.WithRequestTimeout(4*time.Second)).Then(func(payload any, err error) {
			if err != nil {
				consumer.replies <- promptactor.Assembled{
					ID:  incoming.Input.ID,
					Err: err,
				}
				return
			}
			consumer.replies <- payload.(promptactor.Assembled)
		})
	}
}
