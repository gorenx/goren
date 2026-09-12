package companionactor

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentregistry "github.com/gorenx/goren/agent/registry"
	sessionactor "github.com/gorenx/goren/session/actor"
	sessionregistry "github.com/gorenx/goren/session/registry"
	"github.com/gorenx/goren/settings"
	companions "github.com/gorenx/goren/subagent/companion"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type settingsFault struct {
	settings.Settings
	failure error
}

func (source *settingsFault) UpdateAgent(ctx context.Context, name string, expected int64, configuration settings.Values) (settings.Snapshot, error) {
	if source.failure != nil {
		failure := source.failure
		source.failure = nil
		return settings.Snapshot{}, failure
	}
	return source.Settings.UpdateAgent(ctx, name, expected, configuration)
}

func TestCompanionConfigurationSavePrecedesRelationshipAndRetryReusesName(t *testing.T) {
	for _, failureAt := range []string{"settings", "relationship-flush"} {
		t.Run(failureAt, func(t *testing.T) {
			fixture := newStewardship(t)
			fixture.stopSteward(t)
			conversation := request[sessionregistry.Endpoint](t, fixture.system, fixture.sessions, sessionregistry.ResolveSession{
				SessionID:  fixture.input.Creation.AgentID,
				CreationID: fixture.input.Creation.CreationID,
			})
			directory := fixture.creator.settings
			target := conversation.Target
			if failureAt == "settings" {
				directory = &settingsFault{
					Settings: directory,
					failure:  errInjected,
				}
			} else {
				target = faultRoute(t, fixture, faultSetup{
					target: target,
					flush:  true,
				})
			}
			endpoint, err := acceptSteward(t.Context(), fixture.system, start{
				Parent:   fixture.input.Creation,
				Registry: fixture.authority,
				Session:  target,
				Creator:  fixture.creator,
				Settings: directory,
			})
			if err != nil {
				t.Fatal(err)
			}
			awaitSteward(t, endpoint, Ready)
			fixture.client = endpoint
			input := Create{
				Name:    "reviewer",
				Enabled: true,
				Initial: settings.Values{
					Prompts: map[string]settings.PromptConfig{"default": {
						Persona: new("private configuration marker"),
					}},
				},
			}
			failed, err := endpoint.Create(t.Context(), input)
			if !errors.Is(err, errInjected) || failed.Persisted || failed.Creation.CreationID != "" {
				t.Fatalf("failed save published or started child: %+v, %v", failed, err)
			}
			missing := request[agentregistry.Ready](t, fixture.system, fixture.authority, agentregistry.FindAgent{
				AgentID: failed.Companion.ChildSessionID,
			})
			if missing.Err == nil {
				t.Fatal("configuration failure created child resources")
			}
			before, err := fixture.creator.settings.Describe(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if failureAt == "settings" && len(before.Target.Agents) != 0 {
				t.Fatal("failed Settings save changed configuration")
			}
			accepted, err := endpoint.Create(t.Context(), input)
			if err != nil || accepted.Companion.Configuration != failed.Companion.Configuration || accepted.Companion.ChildSessionID != failed.Companion.ChildSessionID {
				t.Fatalf("retry changed allocated names: %+v, %v", accepted, err)
			}
			awaitPhase(t, endpoint, "reviewer", Ready)
			after, err := fixture.creator.settings.Describe(t.Context())
			if err != nil || len(after.Target.Agents) != 1 || after.State.Revision != 1 || len(after.Target.Companions) != 0 {
				t.Fatalf("retry duplicated config or exposed a startup definition: %+v, %v", after, err)
			}
			if *after.Target.Agents[accepted.Companion.Configuration].Prompts["default"].Persona != *input.Initial.Prompts["default"].Persona {
				t.Fatal("Settings did not retain frozen configuration")
			}
			history := request[sessionactor.View](t, fixture.system, conversation.Target, sessionactor.Read{
				CreationID: fixture.input.Creation.CreationID,
			})
			if history.Err != nil {
				t.Fatal(history.Err)
			}
			count := 0
			for _, event := range history.Snapshot.Events {
				if event.Type != "companion/instance" {
					continue
				}
				count++
				if strings.Contains(string(event.Data), *input.Initial.Prompts["default"].Persona) || strings.Contains(string(event.Data), `"initial"`) || !strings.Contains(string(event.Data), accepted.Companion.Configuration) {
					t.Fatal("Session relationship contains configuration values or lost its reference")
				}
			}
			if count != 1 {
				t.Fatalf("relationship retry wrote %d records", count)
			}
		})
	}
}

func TestCompanionRestartReadsItsNamedConfiguration(t *testing.T) {
	fixture := newStewardship(t)
	if _, err := fixture.client.Create(t.Context(), Create{
		Name:    "reviewer",
		Enabled: true,
		Initial: settings.Values{
			Prompts: map[string]settings.PromptConfig{"default": {
				Persona: new("original independent prompt"),
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	first := awaitPhase(t, fixture.client, "reviewer", Ready)
	assertPrompt := func(wanted string) {
		t.Helper()
		target, err := goakt.GrainOf[*promptactor.RuntimeGrain](t.Context(), fixture.system, promptactor.RuntimeName(string(first.Companion.ChildSessionID)),
			goakt.WithLongLivedGrain(),
			goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))),
		)
		if err != nil {
			t.Fatal(err)
		}
		current := request[promptactor.RuntimeView](t, fixture.system, target, promptactor.ReadRuntime{})
		if current.Err != nil || current.Config.Persona != wanted {
			t.Fatalf("actual Prompt Runtime = %+v, want %q", current, wanted)
		}
	}
	assertPrompt("original independent prompt")
	if _, err := fixture.creator.settings.UpdateAgent(t.Context(), "reviewer-default", 0, settings.Values{
		Prompts: map[string]settings.PromptConfig{"default": {
			Persona: new("changed reusable configuration"),
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.creator.settings.UpdateCompanion(t.Context(), "reviewer", 0, companions.Config{
		Enabled:       true,
		Configuration: "reviewer-default",
	}); err != nil {
		t.Fatal(err)
	}

	identifier := settings.Group{
		Section: settings.AgentSection,
		Name:    first.Companion.Configuration,
	}
	current, err := fixture.creator.settings.Describe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.creator.settings.UpdateAgent(t.Context(), identifier.Name, current.State.Version(identifier), settings.Values{
		Prompts: map[string]settings.PromptConfig{"default": {
			Persona: new("updated independent prompt"),
		}},
	}); err != nil {
		t.Fatal(err)
	}
	assertPrompt("original independent prompt")
	if _, err := fixture.client.Restart(t.Context(), Restart{
		Name:               "reviewer",
		ExpectedRevision:   first.Companion.Revision,
		ExpectedCreationID: first.Creation.CreationID,
	}); err != nil {
		t.Fatal(err)
	}
	restored := awaitPhase(t, fixture.client, "reviewer", Ready)
	if restored.Creation == first.Creation || restored.Companion.Configuration != first.Companion.Configuration {
		t.Fatal("restart replaced the configuration reference or reused the old lifetime")
	}
	assertPrompt("updated independent prompt")
}
