package toolactor_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/toolaskuser"
	"github.com/gorenx/goren/tools/tool"
	toolactor "github.com/gorenx/goren/tools/tool/actor"
	"github.com/gorenx/goren/userquestions"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
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

func startTool(t *testing.T, source tool.Provider) (goakt.ActorSystem, *goakt.GrainIdentity) {
	t.Helper()
	ctx := context.Background()
	constructors, err := toolactor.NewProviders(tool.Providers{toolaskuser.Name: source})
	if err != nil {
		t.Fatal(err)
	}
	system, err := goakt.NewActorSystem("tool-registration", goakt.WithPubSub(), goakt.WithLogger(log.DiscardLogger), goakt.WithExtensions(constructors))
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
	target, err := goakt.GrainOf[*toolactor.Grain](ctx, system, "conversation.tool.ask_user_question",
		goakt.WithLongLivedGrain(),
		goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	return system, target
}

func initializeTool(t *testing.T, system goakt.ActorSystem, target *goakt.GrainIdentity) {
	t.Helper()
	result := ask[toolactor.Completion](t, system, target, toolactor.Initialize{
		CreationID: "creation",
		AgentID:    "conversation",
		Name:       toolaskuser.Name,
		Authority:  toolAuthority(t, system),
	})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
}

type authority struct {
	instanceID string
}

func (*authority) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*authority) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *authority) OnReceive(gc *goakt.GrainContext) {
	input, valid := gc.Message().(toolactor.Claim)
	if !valid {
		gc.Unhandled()
		return
	}
	var err error
	if input.CreationID != "creation" || input.AgentID != "conversation" || input.Name != toolaskuser.Name ||
		(owner.instanceID != "" && owner.instanceID != input.InstanceID) {
		err = errors.New("test authority: claim rejected")
	} else {
		owner.instanceID = input.InstanceID
	}
	gc.Response(toolactor.Completion{
		Err: err,
	})
}

func toolAuthority(t *testing.T, system goakt.ActorSystem) *goakt.GrainIdentity {
	t.Helper()
	identity, err := goakt.GrainOf[*authority](context.Background(), system, "tool-authority", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	return identity
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &waitingProvider{
		entered: make(chan struct{}),
	}
	system, target := startTool(t, source)
	initializeTool(t, system, target)
	result := make(chan error, 1)
	go func() {
		payload, err := system.AskGrain(context.Background(), target, toolactor.Provision{
			CreationID: "creation",
			Revision:   tool.Revision("cancelled"),
			Schema:     questionSchema(t),
			Context:    ctx,
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

type panickingProvider struct{ opens atomic.Int32 }

func (source *panickingProvider) Open(context.Context) (tool.Implementation, error) {
	source.opens.Add(1)
	panic("provider failure")
}

func TestProviderPanicSettlesRequestAndAllowsCleanup(t *testing.T) {
	source := &panickingProvider{}
	system, target := startTool(t, source)
	initializeTool(t, system, target)
	if reply := ask[toolactor.Completion](t, system, target, toolactor.Provision{
		CreationID: "creation",
		Revision:   tool.Revision("panicked"),
		Schema:     questionSchema(t),
	}); reply.Err == nil {
		t.Fatal("provider panic was not reported")
	}
	if source.opens.Load() != 1 {
		t.Fatal("panic test never reached Provider.Open")
	}
	if reply := ask[toolactor.Completion](t, system, target, toolactor.Close{
		CreationID: "creation",
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
}

func questionSchema(t *testing.T) llm.ToolSchema {
	t.Helper()
	definition, err := toolaskuser.NewDefinition(questions{})
	if err != nil {
		t.Fatal(err)
	}
	return llm.ToolSchema{
		Name:        definition.Name,
		Description: definition.Description,
		Parameters:  definition.Parameters,
	}
}
