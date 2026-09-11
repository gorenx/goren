package registry_test

import (
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func TestChildCreationSelectsAndOwnsAdditionalComponents(t *testing.T) {
	for _, selection := range []string{"inherited", "subset", "empty", "duplicate", "nil-owner"} {
		t.Run(selection, func(t *testing.T) {
			system, authority, input := creationFixture(t)
			events := make(chan string, 16)
			for _, name := range []string{"first", "second"} {
				route, err := goakt.GrainOf[*componentOwner](t.Context(), system, name, goakt.WithLongLivedGrain())
				if err != nil {
					t.Fatal(err)
				}
				askLifecycle[struct{}](t, system, route, componentBehavior{
					events: events,
				})
				input.Components = append(input.Components, route)
			}
			parent := askLifecycle[agentregistry.Ready](t, system, authority, input)
			if parent.Err != nil {
				t.Fatal(parent.Err)
			}
			expect := func(wanted ...string) {
				t.Helper()
				for _, name := range wanted {
					select {
					case received := <-events:
						if received != name {
							t.Fatalf("component event = %q, want %q", received, name)
						}
					default:
						t.Fatalf("confirmed lifecycle omitted %q", name)
					}
				}
				select {
				case received := <-events:
					t.Fatalf("unexpected component operation %q", received)
				default:
				}
			}
			expect("start-first", "start-second")
			child := agentregistry.CreateChildAgent{
				Role:   agent.CompanionRole,
				Parent: parent.Creation,
				Child: agentregistry.Creation{
					AgentID:    "child",
					CreationID: "child-first",
				},
			}
			var owners []*goakt.GrainIdentity
			switch selection {
			case "inherited":
				owners = input.Components
			case "subset":
				child.Components = []*goakt.GrainIdentity{input.Components[1]}
				owners = child.Components
			case "empty":
				child.Components = []*goakt.GrainIdentity{}
			case "duplicate":
				child.Components = []*goakt.GrainIdentity{input.Components[0], input.Components[0]}
			case "nil-owner":
				child.Components = []*goakt.GrainIdentity{nil}
			}
			ready := askLifecycle[agentregistry.Ready](t, system, authority, child)
			if selection == "duplicate" || selection == "nil-owner" {
				if ready.Err == nil || ready.Executor != nil {
					t.Fatal("invalid component selection created a child")
				}
				expect()
				// A corrected request with the same identity must succeed: validation
				// must not leave a frozen, failed reservation behind.
				child.Components = []*goakt.GrainIdentity{}
				ready = askLifecycle[agentregistry.Ready](t, system, authority, child)
			}
			if ready.Err != nil || len(ready.Components) != len(owners) {
				t.Fatalf("child components: %+v", ready)
			}
			var starts []string
			for index, route := range owners {
				if !ready.Components[index].Owner.Equal(route) || ready.Components[index].Runtime == nil {
					t.Fatal("Ready exposed a component outside the selected set")
				}
				starts = append(starts, "start-"+route.Name())
			}
			expect(starts...)
			repeated := askLifecycle[agentregistry.Ready](t, system, authority, child)
			if repeated.Err != nil {
				t.Fatal(repeated.Err)
			}
			expect()
			// Changing the caller's slice cannot change the existing installation.
			if len(child.Components) == 1 {
				child.Components[0] = input.Components[0]
			} else {
				child.Components = []*goakt.GrainIdentity{input.Components[0]}
			}
			conflict := askLifecycle[agentregistry.Ready](t, system, authority, child)
			if !errors.Is(conflict.Err, agentregistry.ErrCreation) {
				t.Fatalf("changed component selection accepted: %v", conflict.Err)
			}
			expect()
			if result := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{
				Creation: child.Child,
			}); result.Err != nil {
				t.Fatal(result.Err)
			}
			var stops []string
			for index := len(ready.Components) - 1; index >= 0; index-- {
				stops = append(stops, "stop-"+ready.Components[index].Owner.Name())
			}
			expect(stops...)
			remaining := askLifecycle[agentregistry.Ready](t, system, authority, agentregistry.FindAgent{
				AgentID: parent.Creation.AgentID,
			})
			if remaining.Err != nil || len(remaining.Components) != 2 {
				t.Fatal("child selection changed parent components", remaining.Err)
			}
			if result := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{
				Creation: parent.Creation,
			}); result.Err != nil {
				t.Fatal(result.Err)
			}
			expect("stop-second", "stop-first")
		})
	}
}
