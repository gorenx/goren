package runtimeactor_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/tools/execution"

	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/toolaskuser"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	toolruntime "github.com/gorenx/goren/tools/runtime"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	"github.com/gorenx/goren/tools/tool"
	toolactor "github.com/gorenx/goren/tools/tool/actor"
	"github.com/gorenx/goren/userquestions"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type questions struct{}

func (questions) Ask(context.Context, userquestions.Request) (userquestions.Answer, error) {
	return userquestions.Answer{
		Answers: []userquestions.AnswerItem{},
	}, nil
}

type source struct {
	opens        atomic.Int32
	closes       atomic.Int32
	failure      bool
	closeFailure atomic.Bool
}

type resource struct {
	closes  *atomic.Int32
	failure *atomic.Bool
}

func (owned *resource) Close(context.Context) error {
	owned.closes.Add(1)
	if owned.failure.Load() {
		return errors.New("tool cleanup failed")
	}
	return nil
}
func (provider *source) Open(context.Context) (tool.Implementation, error) {
	provider.opens.Add(1)
	if provider.failure {
		return tool.Implementation{}, errors.New("tool construction failed")
	}
	definition, err := toolaskuser.NewDefinition(questions{})
	return tool.Implementation{
		Definition: definition,
		Resource: &resource{
			closes:  &provider.closes,
			failure: &provider.closeFailure,
		},
	}, err
}

func TestUpdateUsesToolLifecycleForReplacementRollbackAndRemoval(t *testing.T) {
	system, directory, catalog := startCatalog(t, true)
	ctx := context.Background()
	authority, err := goakt.GrainOf[*agentregistry.Grain](ctx, system, agentregistry.Name,
		goakt.WithLongLivedGrain(),
		goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	creation := agentregistry.Creation{
		AgentID:    "agent",
		CreationID: "creation",
	}
	if reply := ask[agentregistry.Completion](t, system, authority, agentregistry.ReserveAgent{
		Creation: creation,
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	t.Cleanup(func() {
		if reply := ask[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{
			Creation: creation,
		}); reply.Err != nil {
			t.Error(reply.Err)
		}
	})
	instances, err := runtimeactor.NewInstances(system, authority, directory, creation.AgentID, creation.CreationID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if reply := ask[catalogactor.Completion](t, system, directory, catalogactor.InitializeDefaults{}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	business, err := toolruntime.NewRuntime(creation.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := business.Snapshot(); err == nil {
		t.Fatal("unconfigured runtime exposed a snapshot")
	}
	first := &source{}
	replacement := &source{}
	t.Cleanup(func() { replacement.closeFailure.Store(false) })
	broken := &source{
		failure: true,
	}
	scenarios := []struct {
		id toolregistry.UpdateID

		provider *source
		fails    bool
	}{
		{
			id:       "first",
			provider: first,
		},
		{
			id: "replace",

			provider: replacement,
		},
		{
			id: "rollback",

			provider: broken,
			fails:    true,
		},
		{
			id: "remove",
		},
	}
	var original tool.Identity
	for _, scenario := range scenarios {
		var entries []toolregistry.Tool
		if scenario.provider != nil {
			entries = []toolregistry.Tool{{
				Name:     toolaskuser.Name,
				Provider: scenario.provider,
				Visible:  true,
			}}
		}
		if scenario.id == "first" {
			err = business.Initialize(ctx, scenario.id, entries, catalog, instances)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			configured := ask[catalogactor.Configuration](t, system, directory, catalogactor.RegisterConfiguration{
				ID:        scenario.id,
				AgentID:   creation.AgentID,
				Overrides: entries,
			})
			if configured.Err != nil {
				t.Fatal(configured.Err)
			}
		}
		if scenario.id == "remove" {
			replacement.closeFailure.Store(true)
			complete, err := business.Apply(ctx, scenario.id, catalog, instances)
			if complete || err == nil || replacement.closes.Load() != 1 {
				t.Fatalf("failed removal released update barrier: complete=%v error=%v", complete, err)
			}
			if _, found, err := catalog.Lookup(ctx, scenario.id); err != nil || !found {
				t.Fatalf("failed cleanup discarded publication: found=%v error=%v", found, err)
			}
			if _, err := business.Snapshot(); err == nil {
				t.Fatal("unfinished removal exposed a usable snapshot")
			}
			if complete, err := business.Apply(ctx, "other", catalog, instances); complete || err == nil {
				t.Fatal("another update bypassed unfinished removal")
			}
			replacement.closeFailure.Store(false)
		}
		complete, err := business.Apply(ctx, scenario.id, catalog, instances)
		if !complete || (err != nil) != scenario.fails {
			t.Fatalf("%s complete=%v error=%v", scenario.id, complete, err)
		}
		published := ask[catalogactor.Catalog](t, system, directory, catalogactor.ReadDefinitions{
			AgentID: creation.AgentID,
		})
		if published.Err != nil || !published.Found || published.Definitions.AgentID != creation.AgentID {
			t.Fatalf("%s publication = %#v", scenario.id, published)
		}
		if scenario.id != "remove" {
			snapshot, failure := business.Snapshot()
			if failure != nil {
				t.Fatal(failure)
			}
			if scenario.id == "first" {
				instance := ask[agentactor.ToolInstance](t, system, authority, agentactor.CreateTool{
					AgentID:         creation.AgentID,
					AgentCreationID: creation.CreationID,
					Name:            toolaskuser.Name,
					Registry:        directory,
					Timeout:         time.Second,
				})
				description := ask[toolactor.Description](t, system, instance.Identity, toolactor.Describe{
					CreationID: instance.CreationID,
				})
				if description.Err != nil || description.Implementation == "" || string(description.Implementation) == string(scenario.id) {
					t.Fatalf("execution identity: %+v", description)
				}
				original = description.Implementation
			}
			coordinator, failure := instances.Scheduler(ctx, snapshot, execution.Policies{})
			if failure != nil {
				t.Fatal(failure)
			}
			prepared, failure := coordinator.PrepareCall(ctx, tools.ToolExecutionInput{
				CallID:    "call-" + agentmessage.CallID(scenario.id),
				Name:      toolaskuser.Name,
				SessionID: creation.AgentID,
				Arguments: []byte(`{"questions":[{"id":"q","question":"Continue?"}]}`),
			})
			if failure != nil {
				t.Fatal(failure)
			}
			failure = prepared.Dispatch()
			if failure != nil {
				t.Fatal(failure)
			}
			failure = prepared.Finalize()
			if failure != nil {
				t.Fatal(failure)
			}
			result, failure := prepared.Result()
			if failure != nil || result == nil || result.Failed() {
				t.Fatalf("real ToolActor execution failed: %+v %v", result, failure)
			}
		}
		if scenario.id != "remove" && (len(published.Definitions.Schemas) != 1 || published.Definitions.Schemas[0].Version != 1) {
			t.Fatal("unchanged schema version changed during implementation replacement", published)
		}
		if scenario.id == "remove" && len(published.Definitions.KnownNames) != 0 {
			t.Fatal("removed tool remained in catalog", published)
		}
		settled, err := business.Snapshot()
		if err != nil {
			t.Fatalf("settled snapshot = %#v, %v", settled, err)
		}
		if len(settled.Names) != 0 {
			settled.Names[0] = "mutated"
			copied, err := business.Snapshot()
			if err != nil || copied.Names[0] != toolaskuser.Name {
				t.Fatal("snapshot exposed owned membership")
			}
		}
		repeated, err := business.Apply(ctx, scenario.id, catalog, instances)
		if !repeated || (err != nil) != scenario.fails {
			t.Fatalf("completed update retry = %v, %v", repeated, err)
		}
		if scenario.id == "replace" {
			instance := ask[agentactor.ToolInstance](t, system, authority, agentactor.CreateTool{
				AgentID:         creation.AgentID,
				AgentCreationID: creation.CreationID,
				Name:            toolaskuser.Name,
				Registry:        directory,
				Timeout:         time.Second,
			})
			if instance.Err != nil {
				t.Fatal(instance.Err)
			}
			request := toolactor.Execute{
				CreationID:     instance.CreationID,
				Implementation: original,
				Invocation: tools.Invocation{
					CallID:    "old-implementation",
					SessionID: creation.AgentID,
					Name:      toolaskuser.Name,
					Arguments: []byte(`{"questions":[{"id":"q","question":"Continue?"}]}`),
				},
			}
			rejected := ask[tools.BodyResult](t, system, instance.Identity, request)
			if rejected.Err == nil {
				t.Fatal("old implementation remained executable after replacement")
			}
			description := ask[toolactor.Description](t, system, instance.Identity, toolactor.Describe{
				CreationID: instance.CreationID,
			})
			if description.Err != nil || description.Implementation == original {
				t.Fatalf("replacement reused execution identity: %+v", description)
			}
			request.Implementation = description.Implementation
			request.Invocation.CallID = "new-implementation"
			accepted := ask[tools.BodyResult](t, system, instance.Identity, request)
			if accepted.Err != nil {
				t.Fatal("new implementation could not execute", accepted.Err)
			}
		}
		if scenario.id == "replace" && (first.closes.Load() != 1 || replacement.closes.Load() != 0) {
			t.Fatal("replacement did not release only the old implementation")
		}
		if scenario.fails && replacement.closes.Load() != 0 {
			t.Fatal("rollback closed the current implementation")
		}
	}
	if first.opens.Load() != 1 || first.closes.Load() != 1 ||
		replacement.opens.Load() != 1 || replacement.closes.Load() != 2 ||
		broken.opens.Load() != 1 || broken.closes.Load() != 0 {
		t.Fatal("unexpected tool acquisition or release counts")
	}
}
