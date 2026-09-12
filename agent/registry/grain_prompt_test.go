package registry_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func TestCreationPassesIndependentPromptWithoutImplicitChildInheritance(t *testing.T) {
	system, authority, input := creationFixture(t)
	input.Prompt = &systemprompt.Config{
		Persona: "parent configured persona",
	}
	parent := askLifecycle[agentregistry.Ready](t, system, authority, input)
	if parent.Err != nil {
		t.Fatal(parent.Err)
	}
	t.Cleanup(func() {
		if result := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{
			Creation: parent.Creation,
		}); result.Err != nil {
			t.Error(result.Err)
		}
	})
	for _, scenario := range []struct {
		name     session.SessionID
		settings *systemprompt.Config
		expected string
	}{
		{
			name: "explicit",
			settings: &systemprompt.Config{
				Persona: "child configured persona",
			},
			expected: "child configured persona",
		},
		{
			name:     "defaults",
			settings: &systemprompt.Config{},
		},
		{
			name: "shared",
		},
	} {
		t.Run(string(scenario.name), func(t *testing.T) {
			child := agentregistry.CreateChildAgent{
				Parent: parent.Creation,
				Child: agentregistry.Creation{
					AgentID:    scenario.name,
					CreationID: string(scenario.name) + "-creation",
				},
				Role:   agent.CompanionRole,
				Prompt: scenario.settings,
				PromptSections: []systemprompt.PromptSection{
					{
						Name: "child-duty",
						Text: systemprompt.StaticText("child duty remains"),
					},
				},
			}
			ready := askLifecycle[agentregistry.Ready](t, system, authority, child)
			if ready.Err != nil {
				t.Fatal(ready.Err)
			}
			identity, err := goakt.GrainOf[*promptactor.RuntimeGrain](t.Context(), system, promptactor.RuntimeName(string(scenario.name)),
				goakt.WithLongLivedGrain(),
				goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))),
			)
			if err != nil {
				t.Fatal(err)
			}
			view := askLifecycle[promptactor.RuntimeView](t, system, identity, promptactor.ReadRuntime{})
			if view.Err != nil || view.Config.Persona != scenario.expected {
				t.Fatalf("creation lost explicit prompt: %+v", view)
			}
			// ApplyTools checks the actual installed prompt runtime without requiring
			// an artificial model invocation in this Registry lifecycle fixture.
			applied := askLifecycle[promptactor.Completion](t, system, identity, promptactor.ApplyTools{
				AgentID: scenario.name,
			})
			if applied.Err != nil {
				t.Fatal(applied.Err)
			}
			child.Prompt = &systemprompt.Config{
				Persona: "replacement must be refused",
			}
			repeated := askLifecycle[agentregistry.Ready](t, system, authority, child)
			if !errors.Is(repeated.Err, agentregistry.ErrCreation) {
				t.Fatalf("repeated creation replaced frozen configuration: %v", repeated.Err)
			}
			view = askLifecycle[promptactor.RuntimeView](t, system, identity, promptactor.ReadRuntime{})
			if view.Err != nil || strings.Contains(view.Config.Persona, "parent") || view.Config.Persona != scenario.expected {
				t.Fatalf("child adopted unrelated configuration: %+v", view)
			}
		})
	}
}
