package registry

import (
	"context"
	"testing"

	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type selectionGrain struct{}

func (*selectionGrain) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*selectionGrain) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (*selectionGrain) OnReceive(*goakt.GrainContext)                         {}

func TestCreationMatchesMiddlewareSelection(t *testing.T) {
	system, err := goakt.NewActorSystem("creation-selection", goakt.WithLogger(log.DiscardLogger))
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
	first, err := goakt.GrainOf[*selectionGrain](t.Context(), system, "first")
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := goakt.GrainOf[*selectionGrain](t.Context(), system, "first")
	if err != nil {
		t.Fatal(err)
	}
	other, err := goakt.GrainOf[*selectionGrain](t.Context(), system, "other")
	if err != nil {
		t.Fatal(err)
	}
	for _, boundary := range []string{"pre-step", "request-error", "turn-stopping"} {
		t.Run(boundary, func(t *testing.T) {
			create := func(routes []middlewareactor.Route) CreateAgent {
				input := CreateAgent{}
				switch boundary {
				case "pre-step":
					input.Handlers.PreStep = routes
				case "request-error":
					input.Handlers.RequestError = routes
				case "turn-stopping":
					input.Handlers.TurnStopping = routes
				}
				return input
			}
			original := create([]middlewareactor.Route{
				{
					Definition: "first",
					Target:     first,
				},
				{
					Definition: "second",
					Target:     other,
				},
			})
			for _, sample := range []struct {
				name   string
				routes []middlewareactor.Route
				match  bool
			}{
				{
					name: "same identity",
					routes: []middlewareactor.Route{
						{
							Definition: "first",
							Target:     repeated,
						},
						{
							Definition: "second",
							Target:     other,
						},
					},
					match: true,
				},
				{
					name: "different target",
					routes: []middlewareactor.Route{
						{
							Definition: "first",
							Target:     other,
						},
						{
							Definition: "second",
							Target:     first,
						},
					},
				},
				{
					name: "missing selection",
				},
			} {
				t.Run(sample.name, func(t *testing.T) {
					if actual := original.matches(create(sample.routes)); actual != sample.match {
						t.Fatalf("matches = %v, want %v", actual, sample.match)
					}
				})
			}
			if !create(nil).matches(create([]middlewareactor.Route{})) {
				t.Fatal("empty selections should both use defaults")
			}
		})
	}
}
