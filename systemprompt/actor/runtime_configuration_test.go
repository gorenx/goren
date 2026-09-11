package promptactor_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func TestRuntimeConfigurationRefreshesBaselineOnlyAtTurnBoundary(t *testing.T) {
	endpoint, running, registry := startRegistry(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	replies := make(chan initializationReply, 1)
	requester, err := running.Spawn(ctx, "configuration-requester", &configurationRequester{
		replies: replies,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	request := func(target *goakt.GrainIdentity, input any) (any, error) {
		t.Helper()
		if err := goakt.Tell(ctx, requester, configurationRequest{
			target: target,
			input:  input,
		}); err != nil {
			return nil, err
		}
		select {
		case reply := <-replies:
			return reply.payload, reply.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := endpoint.ApplyBaseline(ctx, systemprompt.Config{
		Persona: "old global persona",
	}); err != nil {
		t.Fatal(err)
	}
	before, err := endpoint.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := endpoint.ApplyBaseline(ctx, systemprompt.Config{
		ToolOrder: []string{},
	}); err == nil {
		t.Fatal("invalid baseline accepted")
	}
	unchanged, err := endpoint.Prepare(ctx)
	if err != nil || unchanged.Version != before.Version {
		t.Fatalf("failed baseline changed version: %#v %v", unchanged, err)
	}
	targets := make([]*goakt.GrainIdentity, 0, 2)
	for _, identifier := range []session.SessionID{"global-agent", "local-agent"} {
		tools, err := goakt.GrainOf[*initializationRegistry](ctx, running, "tools-"+string(identifier), goakt.WithLongLivedGrain())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := running.AskGrain(ctx, tools, promptDefinitions{
			agentID: identifier,
		}, time.Second); err != nil {
			t.Fatal(err)
		}
		target, err := goakt.GrainOf[*promptactor.RuntimeGrain](ctx, running, promptactor.RuntimeName(string(identifier)), goakt.WithLongLivedGrain())
		if err != nil {
			t.Fatal(err)
		}
		sections := []systemprompt.PromptSection{}
		if identifier == "local-agent" {
			sections = append(sections, systemprompt.PromptSection{
				Name: systemprompt.PersonaSection,
				Text: systemprompt.StaticText("local agent persona"),
			})
		}
		payload, err := request(target, promptactor.InitializeRuntime{
			AgentID:        identifier,
			PromptRegistry: registry,
			ToolsRuntime:   tools,
			CreationID:     "creation",
			Sections:       sections,
		})
		if err != nil || payload.(promptactor.RuntimeInitialized).Err != nil {
			t.Fatalf("initialize: %v %v", payload.(promptactor.RuntimeInitialized).Err, err)
		}
		targets = append(targets, target)
	}
	assemble := func(target *goakt.GrainIdentity) string {
		t.Helper()
		payload, err := request(target, promptactor.Assemble{
			ID: "request",
		})
		if err != nil {
			t.Fatal(err)
		}
		result := payload.(promptactor.Assembled)
		if result.Err != nil {
			t.Fatal(result.Err)
		}
		output, err := systemprompt.RenderPrompt(result.Prompt)
		if err != nil {
			t.Fatal(err)
		}
		return output
	}
	if err := endpoint.ApplyBaseline(ctx, systemprompt.Config{
		Persona: "new global persona",
	}); err != nil {
		t.Fatal(err)
	}
	payload, err := request(targets[0], promptactor.ReadRuntime{})
	if err != nil || payload.(promptactor.RuntimeView).Config.Persona != "old global persona" {
		t.Fatalf("query exposed unadopted shared config: %#v %v", payload, err)
	}
	// A tool notification must not switch the shared prompt baseline mid-Turn.
	payload, err = running.AskGrain(ctx, targets[0], promptactor.ApplyTools{
		AgentID: "global-agent",
	}, time.Second)
	if err != nil || payload.(promptactor.Completion).Err != nil {
		t.Fatalf("apply tools: %#v %v", payload, err)
	}
	if actual := assemble(targets[0]); !strings.Contains(actual, "old global persona") {
		t.Fatalf("shared update changed existing Turn: %s", actual)
	}
	for index, identifier := range []session.SessionID{"global-agent", "local-agent"} {
		payload, err := request(targets[index], promptactor.ApplyConfiguration{
			AgentID: identifier,
		})
		if err != nil || payload.(promptactor.Completion).Err != nil {
			t.Fatalf("configure: %#v %v", payload, err)
		}
	}
	if actual := assemble(targets[0]); !strings.Contains(actual, "new global persona") || strings.Contains(actual, "old global persona") {
		t.Fatalf("next Turn missed baseline: %s", actual)
	}
	if actual := assemble(targets[1]); !strings.Contains(actual, "local agent persona") || strings.Contains(actual, "new global persona") {
		t.Fatalf("baseline overwrote local persona: %s", actual)
	}
}

type configurationRequest struct {
	target *goakt.GrainIdentity
	input  any
}

type configurationRequester struct {
	replies chan initializationReply
}

func (*configurationRequester) PreStart(*goakt.Context) error { return nil }
func (*configurationRequester) PostStop(*goakt.Context) error { return nil }
func (owner *configurationRequester) Receive(rc *goakt.ReceiveContext) {
	if incoming, ok := rc.Message().(configurationRequest); ok {
		rc.RequestGrain(incoming.target, incoming.input, goakt.WithRequestTimeout(time.Second)).Then(func(payload any, failure error) {
			owner.replies <- initializationReply{
				payload: payload,
				err:     failure,
			}
		})
	}
}
