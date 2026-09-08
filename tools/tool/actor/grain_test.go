package toolactor_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/toolaskuser"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	"github.com/gorenx/goren/tools/tool"
	toolactor "github.com/gorenx/goren/tools/tool/actor"
	"github.com/gorenx/goren/userquestions"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type questions struct{}

func (questions) Ask(context.Context, userquestions.Request) (userquestions.Answer, error) {
	return userquestions.Answer{}, nil
}

type provider struct {
	opens     atomic.Int32
	closes    atomic.Int32
	questions userquestions.UserQuestions
}

type resource struct {
	count *atomic.Int32
}

func (owned resource) Close(context.Context) error {
	owned.count.Add(1)
	return nil
}

func (source *provider) Open(context.Context) (tool.Implementation, error) {
	source.opens.Add(1)
	service := source.questions
	if service == nil {
		service = questions{}
	}
	definition, err := toolaskuser.NewDefinition(service)
	return tool.Implementation{
		Definition: definition,
		Resource: resource{
			count: &source.closes,
		},
	}, err
}

func ask[T any](t *testing.T, system goakt.ActorSystem, target *goakt.GrainIdentity, message any) T {
	t.Helper()
	payload, err := system.AskGrain(context.Background(), target, message, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := payload.(T)
	if !ok {
		t.Fatalf("unexpected reply %T", payload)
	}
	return result
}

func startTool(t *testing.T) (goakt.ActorSystem, *goakt.GrainIdentity, *goakt.GrainIdentity) {
	t.Helper()
	ctx := context.Background()
	system, err := goakt.NewActorSystem("tool-registration", goakt.WithPubSub(), goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := system.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	directory, err := goakt.GrainOf[*catalogactor.RegistryGrain](ctx, system, catalogactor.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	target, err := goakt.GrainOf[*toolactor.Grain](ctx, system, "conversation.tool.ask_user_question", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	if result := ask[toolactor.Completion](t, system, target, toolactor.Initialize{
		CreationID: "creation",
		AgentID:    "conversation",
		Name:       toolaskuser.Name,
		Registry:   directory,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	return system, directory, target
}

func TestProvisionRegistersCandidateAndRetriesWithoutReopening(t *testing.T) {
	system, directory, target := startTool(t)
	ctx := context.Background()
	source := &provider{}
	input := toolactor.Provision{
		CreationID: "creation",
		Revision: tool.Revision{
			ID:      "update",
			Version: 1,
		},
		Provider: source,
		Visible:  true,
		Context:  ctx,
	}
	// Construction succeeds but registration cannot: the catalog batch is absent.
	if result := ask[toolactor.Completion](t, system, target, input); result.Err == nil {
		t.Fatal("registration against a missing batch succeeded")
	}
	if result := ask[catalogactor.Completion](t, system, directory, catalogactor.Begin{
		Update: toolregistry.Update{
			ID:      "update",
			AgentID: "conversation",
			Names:   []string{toolaskuser.Name},
		},
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	for range 2 {
		if result := ask[toolactor.Completion](t, system, target, input); result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	if source.opens.Load() != 1 || source.closes.Load() != 0 {
		t.Fatal("registration retry reconstructed or released the candidate")
	}
	publication := ask[catalogactor.Catalog](t, system, directory, catalogactor.Publish{
		ID: "update",
	})
	if publication.Err != nil ||
		publication.Definitions.Version != 1 ||
		len(publication.Definitions.Definitions.Schemas) != 1 ||
		publication.Definitions.Definitions.Schemas[0].Name != toolaskuser.Name {
		t.Fatalf("candidate was not registered: %#v", publication)
	}
	if result := ask[toolactor.Completion](t, system, target, toolactor.Commit{
		CreationID: "old-creation",
		Revision:   input.Revision,
	}); result.Err == nil {
		t.Fatal("old creation switched the candidate")
	}
	if result := ask[toolactor.Completion](t, system, target, toolactor.Commit{
		CreationID: input.CreationID,
		Revision:   input.Revision,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	for range 2 {
		if result := ask[toolactor.Completion](t, system, target, toolactor.Close{
			CreationID: input.CreationID,
			Context:    ctx,
		}); result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	if source.closes.Load() != 1 {
		t.Fatalf("resource close count=%d", source.closes.Load())
	}
}

type waitingProvider struct {
	entered chan struct{}
	closed  atomic.Int32
}

func (source *waitingProvider) Open(ctx context.Context) (tool.Implementation, error) {
	close(source.entered)
	<-ctx.Done()
	return tool.Implementation{
		Resource: resource{
			count: &source.closed,
		},
	}, context.Cause(ctx)
}

func TestProvisionCancellationWaitsForResourceCleanup(t *testing.T) {
	system, _, target := startTool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &waitingProvider{
		entered: make(chan struct{}),
	}
	result := make(chan error, 1)
	go func() {
		payload, err := system.AskGrain(context.Background(), target, toolactor.Provision{
			CreationID: "creation",
			Revision: tool.Revision{
				ID:      "cancelled",
				Version: 1,
			},
			Provider: source,
			Context:  ctx,
		}, 2*time.Second)
		if err == nil {
			if reply, ok := payload.(toolactor.Completion); ok {
				err = reply.Err
			} else {
				err = errors.New("unexpected completion type")
			}
		}
		result <- err
	}()
	select {
	case <-source.entered:
	case <-time.After(time.Second):
		t.Fatal("construction did not start")
	}
	if reply := ask[toolactor.Completion](t, system, target, toolactor.Close{
		CreationID: "creation",
	}); reply.Err == nil {
		t.Fatal("cleanup was allowed to overlap construction")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || source.closed.Load() != 1 {
			t.Fatalf("reply preceded cleanup or lost cancellation: %v, closes=%d", err, source.closed.Load())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled construction did not settle")
	}
	if reply := ask[toolactor.Completion](t, system, target, toolactor.Close{
		CreationID: "creation",
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
}

func TestProvisionCannotRegisterInAnotherAgentsBatch(t *testing.T) {
	system, directory, target := startTool(t)
	if reply := ask[catalogactor.Completion](t, system, directory, catalogactor.Begin{
		Update: toolregistry.Update{
			ID:      "another-update",
			AgentID: "another-agent",
			Names:   []string{toolaskuser.Name},
		},
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	if reply := ask[toolactor.Completion](t, system, target, toolactor.Provision{
		CreationID: "creation",
		Revision: tool.Revision{
			ID:      "another-update",
			Version: 1,
		},
		Provider: &provider{},
		Visible:  true,
	}); reply.Err == nil {
		t.Fatal("tool registered in another Agent's batch")
	}
	if reply := ask[catalogactor.Catalog](t, system, directory, catalogactor.Publish{
		ID: "another-update",
	}); reply.Err == nil {
		t.Fatal("foreign batch unexpectedly obtained a complete candidate set")
	}
	if reply := ask[toolactor.Completion](t, system, target, toolactor.Close{
		CreationID: "creation",
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
}

type panickingProvider struct{}

func (panickingProvider) Open(context.Context) (tool.Implementation, error) {
	panic("provider failure")
}

func TestProviderPanicSettlesRequestAndAllowsCleanup(t *testing.T) {
	system, _, target := startTool(t)
	if reply := ask[toolactor.Completion](t, system, target, toolactor.Provision{
		CreationID: "creation",
		Revision: tool.Revision{
			ID:      "panicked",
			Version: 1,
		},
		Provider: panickingProvider{},
	}); reply.Err == nil {
		t.Fatal("provider panic was not reported")
	}
	if reply := ask[toolactor.Completion](t, system, target, toolactor.Close{
		CreationID: "creation",
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
}
