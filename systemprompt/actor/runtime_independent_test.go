package promptactor_test

import (
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func exchangePrompt[T any](t *testing.T, system goakt.ActorSystem, target *goakt.GrainIdentity, input any) T {
	t.Helper()
	payload, err := system.AskGrain(t.Context(), target, input, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := payload.(T)
	if !ok {
		t.Fatalf("unexpected response %T", payload)
	}
	return result
}

func TestRuntimeIndependentConfigurationSurvivesSharedChanges(t *testing.T) {
	endpoint, system, registry := startRegistry(t)
	if err := endpoint.ApplyBaseline(t.Context(), systemprompt.Config{
		Persona: "shared before",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := endpoint.AddSection(t.Context(), systemprompt.PromptSection{
		Name: "extension",
		Text: systemprompt.StaticText("registered extension"),
	}); err != nil {
		t.Fatal(err)
	}
	targets := make([]*goakt.GrainIdentity, 0, 2)
	for _, identifier := range []session.SessionID{"review", "research"} {
		baseline, err := goakt.GrainOf[*initializationRegistry](t.Context(), system, "tools-"+string(identifier), goakt.WithLongLivedGrain())
		if err != nil {
			t.Fatal(err)
		}
		exchangePrompt[struct{}](t, system, baseline, promptDefinitions{
			agentID: identifier,
		})
		target, err := goakt.GrainOf[*promptactor.RuntimeGrain](t.Context(), system, promptactor.RuntimeName(string(identifier)),
			goakt.WithLongLivedGrain(),
			goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))),
		)
		if err != nil {
			t.Fatal(err)
		}
		settings := systemprompt.Config{
			Persona:   string(identifier),
			ToolOrder: []string{systemprompt.ToolOrderRest},
		}
		shared := registry
		if identifier == "research" {
			shared = nil
		}
		result := exchangePrompt[promptactor.RuntimeInitialized](t, system, target, promptactor.InitializeRuntime{
			AgentID:    identifier,
			CreationID: "creation",
			Config:     &settings,
			// One independent runtime deliberately has no shared registry route.
			PromptRegistry: shared,
			ToolsRuntime:   baseline,
			Sections: []systemprompt.PromptSection{
				{
					Name: "local-instruction",
					Text: systemprompt.StaticText("retained local instruction"),
				},
			},
		})
		if result.Err != nil {
			t.Fatal(result.Err)
		}
		settings.Persona = "caller mutation"
		settings.ToolOrder[0] = "invalid caller mutation"
		targets = append(targets, target)
	}
	if err := endpoint.ApplyBaseline(t.Context(), systemprompt.Config{
		Persona: "shared after",
	}); err != nil {
		t.Fatal(err)
	}
	assertPrompt := func(index int, expected string) {
		t.Helper()
		result := exchangePrompt[promptactor.Assembled](t, system, targets[index], promptactor.Assemble{
			ID: "verify",
		})
		if result.Err != nil {
			t.Fatal(result.Err)
		}
		output, err := systemprompt.RenderPrompt(result.Prompt)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output, "registered extension") != (index == 0) {
			t.Fatalf("configuration changed the definition source: %s", output)
		}
		if !strings.Contains(output, expected) || !strings.Contains(output, "retained local instruction") || strings.Contains(output, "shared") || strings.Contains(output, "caller mutation") {
			t.Fatalf("incorrect independent prompt: %s", output)
		}
	}
	for index, identifier := range []session.SessionID{"review", "research"} {
		updated := exchangePrompt[promptactor.Completion](t, system, targets[index], promptactor.ApplyConfiguration{
			AgentID: identifier,
		})
		if updated.Err != nil {
			t.Fatal(updated.Err)
		}
		assertPrompt(index, string(identifier))
	}
	rejected := exchangePrompt[promptactor.Completion](t, system, targets[0], promptactor.ApplyConfiguration{
		AgentID: "wrong-agent",
	})
	if rejected.Err == nil {
		t.Fatal("wrong recipient accepted")
	}
	assertPrompt(0, "review")
	assertPrompt(1, "research")
	view := exchangePrompt[promptactor.RuntimeView](t, system, targets[0], promptactor.ReadRuntime{})
	if view.Err != nil || view.Config.Persona != "review" {
		t.Fatalf("read did not return adopted config: %+v", view)
	}
	*view.Config.IncludeHarnessIdentity = false
	view = exchangePrompt[promptactor.RuntimeView](t, system, targets[0], promptactor.ReadRuntime{})
	if view.Config.IncludeHarnessIdentity == nil || !*view.Config.IncludeHarnessIdentity {
		t.Fatal("query exposed mutable config")
	}
}

func TestToolRefreshDoesNotChangeActiveAssembly(t *testing.T) {
	_, system, _ := startRegistry(t)
	baseline, err := goakt.GrainOf[*initializationRegistry](t.Context(), system, "tools-snapshot", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	exchangePrompt[struct{}](t, system, baseline, promptDefinitions{
		agentID: "snapshot",
	})
	target, err := goakt.GrainOf[*promptactor.RuntimeGrain](t.Context(), system, promptactor.RuntimeName("snapshot"),
		goakt.WithLongLivedGrain(),
		goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))),
	)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	initialized := exchangePrompt[promptactor.RuntimeInitialized](t, system, target, promptactor.InitializeRuntime{
		AgentID:    "snapshot",
		CreationID: "creation",
		Config: &systemprompt.Config{
			Persona: "before change",
		},
		ToolsRuntime: baseline,
		Sections: []systemprompt.PromptSection{
			{
				Name: "held-section",
				Text: &blockingText{
					entered: entered,
					release: release,
				},
			},
		},
	})
	if initialized.Err != nil {
		t.Fatal(initialized.Err)
	}
	completed := make(chan promptactor.Assembled, 1)
	requester, err := system.Spawn(t.Context(), "held-prompt", &assemblyRequester{
		replies: completed,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	if err := goakt.Tell(t.Context(), requester, assemblyRequest{
		Target: target,
		Input: promptactor.Assemble{
			ID: "before",
		},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("assembly did not start")
	}
	changed := exchangePrompt[promptactor.Completion](t, system, target, promptactor.ApplyConfiguration{
		AgentID: "snapshot",
	})
	if changed.Err != nil {
		t.Fatal(changed.Err)
	}
	close(release)
	select {
	case result := <-completed:
		if result.Err != nil {
			t.Fatal(result.Err)
		}
		output, err := systemprompt.RenderPrompt(result.Prompt)
		if err != nil || !strings.Contains(output, "before change") || strings.Contains(output, "after change") {
			t.Fatalf("active assembly changed: %s %v", output, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("assembly did not finish")
	}
	result := exchangePrompt[promptactor.Assembled](t, system, target, promptactor.Assemble{
		ID: "after",
	})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	output, err := systemprompt.RenderPrompt(result.Prompt)
	if err != nil || !strings.Contains(output, "before change") || strings.Contains(output, "after change") {
		t.Fatalf("next assembly changed the initialization baseline: %s %v", output, err)
	}
}
