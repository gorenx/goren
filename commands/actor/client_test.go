package commandactor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/commands"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func TestClientUsesCurrentCreationAndPropagatesCancellation(t *testing.T) {
	for _, cancelCall := range []bool{false, true} {
		name := "success"
		if cancelCall {
			name = "cancellation"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			system, err := goakt.NewActorSystem("commands-client", goakt.WithLogger(log.DiscardLogger))
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
			catalog, err := goakt.GrainOf[*Registry](ctx, system, RegistryName, goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			history, err := goakt.GrainOf[*commandHistory](ctx, system, "conversation", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			target, err := goakt.GrainOf[*Grain](ctx, system, RuntimeName("conversation"), goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			handler, err := goakt.GrainOf[*handlerFixture](ctx, system, "handler", goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
			if err != nil {
				t.Fatal(err)
			}
			ask := func(route *goakt.GrainIdentity, input any) any {
				t.Helper()
				result, err := system.AskGrain(ctx, route, input, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			started := make(chan commands.Invocation, 1)
			cancelled := make(chan error, 1)
			ask(handler, bindHandler{
				started:   started,
				cancelled: cancelled,
			})
			if result := ask(catalog, Register{
				ID: "registration",
				Definition: commands.Definition{
					Name:        "compact",
					Description: "Compact history",
				},
				Handler: handler,
			}).(Registered); result.Err != nil {
				t.Fatal(result.Err)
			}
			if result := ask(target, Initialize{
				SessionID:  "conversation",
				CreationID: "current",
				Registry:   catalog,
				Session:    history,
				Timeout:    time.Second,
			}).(Completion); result.Err != nil {
				t.Fatal(result.Err)
			}
			endpoint, err := NewClient(system, target, "current", 100*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := endpoint.List(ctx)
			if err != nil ||
				len(entries) != 1 ||
				entries[0].Name != "compact" {
				t.Fatalf("directory = %#v, %v", entries, err)
			}
			stale, err := NewClient(system, target, "previous", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stale.List(ctx); err == nil {
				t.Fatal("old creation read directory")
			}
			if _, err := stale.Execute(ctx, "/compact", commands.ExecuteOptions{}); err == nil {
				t.Fatal("old creation executed command")
			}
			if result, err := endpoint.Execute(ctx, "/missing", commands.ExecuteOptions{}); err != nil || result != nil {
				t.Fatalf("unknown = %#v, %v", result, err)
			}
			caller, cancel := context.WithCancel(ctx)
			defer cancel()
			finished := make(chan Executed, 1)
			go func() {
				result, err := endpoint.Execute(caller, "/compact", commands.ExecuteOptions{})
				finished <- Executed{
					Execution: result,
					Err:       err,
				}
			}()
			var received commands.Invocation
			select {
			case received = <-started:
			case <-time.After(time.Second):
				t.Fatal("command did not start")
			}
			if received.CommandID == "" || received.SessionID != "conversation" {
				t.Fatalf("invocation = %#v", received)
			}
			// The short query/cancellation timeout must not limit an executing command.
			select {
			case result := <-finished:
				t.Fatalf("command returned before handler: %#v", result)
			case <-time.After(150 * time.Millisecond):
			}
			if cancelCall {
				cancel()
				select {
				case cause := <-cancelled:
					if !errors.Is(cause, context.Canceled) {
						t.Fatalf("cancel cause = %v", cause)
					}
				case <-time.After(time.Second):
					t.Fatal("client did not deliver cancellation")
				}
				select {
				case result := <-finished:
					if !errors.Is(result.Err, context.Canceled) {
						t.Fatalf("client error = %v", result.Err)
					}
				case <-time.After(time.Second):
					t.Fatal("client kept waiting")
				}
				if result := ask(target, Close{
					CreationID: "current",
				}).(Completion); result.Err == nil {
					t.Fatal("cancel acknowledgement allowed premature close")
				}
			}
			ask(handler, releaseHandler{})
			if !cancelCall {
				select {
				case result := <-finished:
					if result.Err != nil ||
						result.Execution == nil ||
						result.Execution.CommandID != received.CommandID {
						t.Fatalf("execution = %#v", result)
					}
				case <-time.After(time.Second):
					t.Fatal("command did not settle")
				}
			}
			deadline := time.Now().Add(time.Second)
			for {
				result := ask(target, Close{
					CreationID: "current",
				}).(Completion)
				if result.Err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("handler cleanup did not complete")
				}
				time.Sleep(time.Millisecond)
			}
			if _, err := endpoint.List(ctx); err == nil {
				t.Fatal("closed owner read directory")
			}
			snapshot := ask(history, sessionactor.Read{}).(sessionactor.View).Snapshot
			if len(snapshot.Events) != 2 ||
				snapshot.Events[0].Type != commands.RunEventName ||
				snapshot.Events[1].Type != commands.DoneEventName {
				t.Fatalf("lifecycle = %#v", snapshot.Events)
			}
		})
	}
}
