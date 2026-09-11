package apiactor_test

import (
	"context"
	"errors"
	modelquery "github.com/gorenx/goren/apiproxy/llm/actor"
	"testing"
	"time"

	llmactor "github.com/gorenx/goren/llm/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// queryEndpoint returns one metadata result directly to the supplied Grain.
// Other commands deliberately withhold replies to verify bridge deadlines.
type queryEndpoint struct{}

func (*queryEndpoint) PreStart(*goakt.Context) error { return nil }
func (*queryEndpoint) PostStop(*goakt.Context) error { return nil }
func (*queryEndpoint) Receive(rc *goakt.ReceiveContext) {
	if envelope, ok := rc.Message().(*goakt.Broadcast); ok {
		if input, ok := envelope.Message().(llmactor.ListModels); ok {
			_ = rc.ActorSystem().TellGrain(rc.Context(), input.ReplyTo, llmactor.ModelsListed{
				RequestID: input.RequestID,
			})
		}
	}
}

func TestQueryBridgeCompletesConcurrentRequestsAndExpiresMissingReplies(t *testing.T) {
	system := startSystem(t)
	ctx := context.Background()
	endpoint, err := system.Spawn(ctx, "queries", &queryEndpoint{})
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := goakt.GrainOf[*modelquery.Grain](ctx, system, "bridge", goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := system.AskGrain(ctx, bridge, modelquery.Initialize{
		Router: endpoint,
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if failure := payload.(llmactor.Completion).Err; failure != nil {
		t.Fatal(failure)
	}
	missing := make(chan response, 1)
	go func() {
		payload, err := system.AskGrain(ctx, bridge, llmactor.ResolveModel{
			Query: llmactor.Query{
				RequestID: "missing",
				Deadline:  time.Now().Add(200 * time.Millisecond),
			},
			Provider: "p",
			Model:    "missing",
		}, time.Second)
		missing <- response{
			payload: payload,
			err:     err,
		}
	}()
	payload, err = system.AskGrain(ctx, bridge, llmactor.ListModels{
		Query: llmactor.Query{
			RequestID: "catalog",
			Deadline:  time.Now().Add(time.Second),
		},
		Provider: "p",
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	listed, ok := payload.(llmactor.ModelsListed)
	if !ok ||
		listed.Err != nil ||
		listed.RequestID == "" {
		t.Fatalf("list reply: %#v", payload)
	}
	result := <-missing
	if result.err != nil {
		t.Fatal(result.err)
	}
	resolved, ok := result.payload.(llmactor.ModelResolved)
	if !ok ||
		!errors.Is(resolved.Err, context.DeadlineExceeded) ||
		resolved.RequestID == listed.RequestID {
		t.Fatalf("missing reply: %#v", result.payload)
	}
}
