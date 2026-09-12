package commandsadapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/commands"
	commandactor "github.com/gorenx/goren/commands/actor"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

// routeFixture tests client protocol mapping; Registry lifecycle and command
// execution behavior remain covered by their owning packages' real-Grain tests.
type routeFixture struct {
	ready agentregistry.Ready
}

func (*routeFixture) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*routeFixture) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *routeFixture) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case agentregistry.Ready:
		owner.ready = input
		gc.Response(true)
	case agentregistry.FindAgent:
		if input.AgentID != "conversation" || !input.WaitReady {
			gc.Response(agentregistry.Ready{
				Err: errors.New("incorrect resolution"),
			})
			return
		}
		gc.Response(owner.ready)
	case commandactor.ListCommands:
		if input.CreationID != "current" {
			gc.Response(commandactor.Listed{
				Err: errors.New("incorrect creation"),
			})
			return
		}
		gc.Response(commandactor.Listed{
			Values: []commands.Descriptor{{
				Name: "compact",
			}},
		})
	default:
		gc.Unhandled()
	}
}

func TestDirectoryBindsAdmittedCreationAndRejectsInvalidRoutes(t *testing.T) {
	ctx := context.Background()
	system, err := goakt.NewActorSystem("command-directory", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := system.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	registry, err := goakt.GrainOf[*routeFixture](ctx, system, "registry", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	target, err := goakt.GrainOf[*routeFixture](ctx, system, "commands", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	directory, err := NewDirectory(system, registry, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	valid := agentregistry.Ready{
		Creation: agentregistry.Creation{
			AgentID:    "conversation",
			CreationID: "current",
		},
		Registration: agent.Registration{
			ID: "conversation",
		},
		Commands: target,
	}
	parent := session.SessionID("parent")
	for _, name := range []string{"valid", "companion", "missing", "identity", "membership", "creation", "child", "failed"} {
		t.Run(name, func(t *testing.T) {
			ready := valid
			switch name {
			case "companion":
				ready.Registration.ParentID = &parent
				ready.Registration.Role = agent.CompanionRole
			case "missing":
				ready.Commands = nil
			case "identity":
				ready.Creation.AgentID = "other"
			case "membership":
				ready.Registration.ID = "other"
			case "creation":
				ready.Creation.CreationID = ""
			case "child":
				ready.Registration.ParentID = &parent
			case "failed":
				ready.Err = errors.New("initialization failed")
			}
			if _, err := system.AskGrain(ctx, registry, ready, time.Second); err != nil {
				t.Fatal(err)
			}
			endpoint, refused := directory.ResolveCommands(ctx, "conversation")
			if name != "valid" && name != "companion" {
				if endpoint != nil || refused == nil {
					t.Fatalf("invalid route accepted: %v, %v", endpoint, refused)
				}
				if name == "child" && refused.Code != connection.ErrorAgentBusy {
					t.Fatalf("child failure: %#v", refused)
				}
				return
			}
			if refused != nil {
				t.Fatal(refused)
			}
			entries, err := endpoint.List(ctx)
			if err != nil || len(entries) != 1 || entries[0].Name != "compact" {
				t.Fatalf("commands: %#v, %v", entries, err)
			}
		})
	}
}
