package runtimeactor_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/toolaskuser"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	"github.com/gorenx/goren/tools/tool"
	toolactor "github.com/gorenx/goren/tools/tool/actor"
)

type contextProvider struct {
	source
	observed atomic.Pointer[tools.ToolRunContext]
}

func (installed *contextProvider) Open(ctx context.Context) (tool.Implementation, error) {
	body, err := installed.source.Open(ctx)
	if err == nil {
		body.Definition.Executor = &contextExecutor{
			target:   body.Definition.Executor,
			observed: &installed.observed,
		}
	}
	return body, err
}

type contextExecutor struct {
	target   tools.Executor
	observed *atomic.Pointer[tools.ToolRunContext]
}

func (body *contextExecutor) Execute(arguments json.RawMessage, runContext tools.ToolRunContext) (json.RawMessage, error) {
	body.observed.Store(&runContext)
	return body.target.Execute(arguments, runContext)
}

func TestRuntimeBindsExecutionToAgentCreationAndRejectsStaleClaim(t *testing.T) {
	installed := &contextProvider{}
	system, directory, _ := startCatalog(t, true, tool.Providers{
		toolaskuser.Name: installed,
	})
	definition, err := toolaskuser.NewDefinition(questions{})
	if err != nil {
		t.Fatal(err)
	}
	configured := ask[catalogactor.Completion](t, system, directory, catalogactor.RegisterSource{
		Name: "local",
		Catalog: toolregistry.Catalog{
			Schemas: []llm.ToolSchema{{
				Name:        definition.Name,
				Description: definition.Description,
				Parameters:  definition.Parameters,
			}},
		},
	})
	if configured.Err != nil {
		t.Fatal(configured.Err)
	}
	target, closing := initializeRuntime(t, system, directory)
	route := ask[catalogactor.ToolAddress](t, system, directory, catalogactor.FindTool{
		AgentID: "agent",
		Name:    toolaskuser.Name,
	})
	if !route.Found {
		t.Fatal("tool not published")
	}
	description := ask[toolactor.Description](t, system, route.Actor, toolactor.Describe{
		CreationID: route.CreationID,
	})
	if description.Err != nil {
		t.Fatal(description.Err)
	}
	result := ask[tools.BodyResult](t, system, route.Actor, toolactor.Execute{
		Context:    tools.WithAgentCreationID(t.Context(), "stale-upstream-agent"),
		CreationID: route.CreationID,
		Revision:   description.Revision,
		Invocation: tools.Invocation{
			CallID:    "call",
			SessionID: "agent",
			Name:      toolaskuser.Name,
			Arguments: json.RawMessage(`{"questions":[{"id":"q","question":"Continue?"}]}`),
		},
	})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	origin := installed.observed.Load()
	if origin == nil {
		t.Fatal("executor did not observe an execution context")
	}
	identifier, found := tools.AgentCreationIDFromContext(origin.Context)
	if !found || origin.Execution.SessionID != "agent" || identifier != closing.CreationID || identifier == route.CreationID {
		t.Fatal("execution context lost the trusted Agent identity")
	}
	for _, selected := range []string{"", "replaced-agent"} {
		rejected := ask[toolactor.Completion](t, system, target, toolactor.Claim{
			AgentCreationID: selected,
			AgentID:         "agent",
			Name:            toolaskuser.Name,
			CreationID:      route.CreationID,
			InstanceID:      "forged",
		})
		if rejected.Err == nil {
			t.Fatal("claim accepted a missing or stale Agent lifetime")
		}
	}
	if reply := ask[runtimeactor.Completion](t, system, target, closing); reply.Err != nil {
		t.Fatal(reply.Err)
	}
}
