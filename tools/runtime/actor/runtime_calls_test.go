package runtimeactor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/toolaskuser"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	toolruntime "github.com/gorenx/goren/tools/runtime"
	"github.com/gorenx/goren/tools/tool"
	toolactor "github.com/gorenx/goren/tools/tool/actor"
	"github.com/gorenx/goren/userquestions"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type descriptorFixture struct {
	queries int
}

type readQueries struct{}

type unusedQuestions struct{}

func (unusedQuestions) Ask(context.Context, userquestions.Request) (userquestions.Answer, error) {
	return userquestions.Answer{}, nil
}

func (*descriptorFixture) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*descriptorFixture) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (fixture *descriptorFixture) OnReceive(gc *goakt.GrainContext) {
	switch gc.Message().(type) {
	case catalogactor.FindTool:
		fixture.queries++
		gc.Response(catalogactor.ToolAddress{
			Found:      true,
			Actor:      gc.Self(),
			CreationID: "creation",
		})
	case toolactor.Describe:
		fixture.queries++
		definition, err := toolaskuser.NewDefinition(unusedQuestions{})
		gc.Response(toolactor.Description{
			Descriptor: definition.Describe(),
			Revision:   tool.Revision("settled"),
			Err:        err,
		})
	case readQueries:
		gc.Response(fixture.queries)
	}
}

func TestRuntimeReusesSettledDescriptors(t *testing.T) {
	system, err := goakt.NewActorSystem("executor-descriptors", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := system.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	route, err := goakt.GrainOf[*descriptorFixture](t.Context(), system, "registry", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	runner := &bodies{
		system:   system,
		registry: route,
		agentID:  "agent",
		timeout:  time.Second,
	}
	membership := toolruntime.Snapshot{
		Names: []string{toolaskuser.Name},
	}
	first, err := prepareScheduler(t.Context(), nil, runner, membership.Names)
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepareScheduler(t.Context(), first, runner, membership.Names)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("compiled contracts were not reused")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := prepareScheduler(cancelled, first, runner, membership.Names); !errors.Is(err, context.Canceled) {
		t.Fatalf("cached descriptors bypassed cancellation: %v", err)
	}
	payload, err := system.AskGrain(t.Context(), route, readQueries{}, time.Second)
	if err != nil || payload != 2 {
		t.Fatalf("descriptors queried again: %v, %v", payload, err)
	}
}
