package commandactor

import (
	"context"
	"errors"
	"github.com/gorenx/goren/commands"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
	"testing"
	"time"
)

type commandHistory struct {
	history   *session.Log
	loseReply string
	commits   map[string]int
}
type loseCommitReply struct{ event string }
type inspectCommits struct{}

func (owner *commandHistory) OnActivate(context.Context, *goakt.GrainProps) error {
	var err error
	owner.commits = make(map[string]int)
	owner.history, err = session.NewLog("conversation", session.CreateOptions{})
	return err
}
func (*commandHistory) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *commandHistory) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case loseCommitReply:
		owner.loseReply = input.event
		gc.Response(true)
	case inspectCommits:
		counts := make(map[string]int)
		for kind, count := range owner.commits {
			counts[kind] = count
		}
		gc.Response(counts)
	case sessionactor.Commit:
		written, err := owner.history.Commit(gc.Context(), input.Plan)
		if err == nil && len(written.Events) > 0 {
			kind := written.Events[0].Type
			owner.commits[kind]++
			if owner.loseReply == kind {
				owner.loseReply = ""
				gc.Err(errors.New("test: reply lost after commit"))
				return
			}
		}
		gc.Response(sessionactor.Committed{
			Result: written,
			Err:    err,
		})
	case sessionactor.Read:
		gc.Response(sessionactor.View{
			Snapshot: owner.history.Snapshot(),
		})
	}
}

type handlerFixture struct {
	started   chan commands.Invocation
	cancelled chan error
	reply     *goakt.GrainReply
}
type bindHandler struct {
	started   chan commands.Invocation
	cancelled chan error
}
type releaseHandler struct{}

func (*handlerFixture) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*handlerFixture) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *handlerFixture) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case bindHandler:
		owner.started = input.started
		owner.cancelled = input.cancelled
		gc.Response(true)
	case Invoke:
		owner.reply = gc.DeferResponse()
		owner.started <- input.Input
	case CancelInvocation:
		owner.cancelled <- input.Cause
		gc.Response(Cancelled{})
	case releaseHandler:
		owner.reply.Response(Completed{
			Result: commands.Result{
				Kind: commands.ResultSuccess,
			},
		})
		gc.Response(true)
	}
}

func TestCommandsRecordLifecycleAndRetainCancelledHandler(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		cancel    bool
		loseReply string
	}{
		{
			name: "success",
		},
		{
			name:   "cancelled",
			cancel: true,
		},
		{
			name:      "run reply lost",
			loseReply: commands.RunEventName,
		},
		{
			name:      "done reply lost",
			loseReply: commands.DoneEventName,
		},
		{
			name:      "cancelled done reply lost",
			cancel:    true,
			loseReply: commands.DoneEventName,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			cancel := scenario.cancel
			ctx := context.Background()
			system, err := goakt.NewActorSystem("commands-execution", goakt.WithLogger(log.DiscardLogger))
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
			ask := func(route *goakt.GrainIdentity, message any) any {
				t.Helper()
				reply, err := system.AskGrain(ctx, route, message, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				return reply
			}
			ask(history, loseCommitReply{
				event: scenario.loseReply,
			})
			started := make(chan commands.Invocation, 1)
			cancelledSignal := make(chan error, 1)
			handler, err := goakt.GrainOf[*handlerFixture](ctx, system, "handler", goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
			if err != nil {
				t.Fatal(err)
			}
			ask(handler, bindHandler{
				started:   started,
				cancelled: cancelledSignal,
			})
			registered := ask(catalog, Register{
				ID: "compact-fixture",
				Definition: commands.Definition{
					Name:        "compact",
					Description: "Compact history",
				},
				Handler: handler,
			}).(Registered)
			if registered.Err != nil {
				t.Fatal(registered.Err)
			}
			initialized := ask(target, Initialize{
				SessionID:  "conversation",
				CreationID: "creation",
				Session:    history,
				Registry:   catalog,
				Timeout:    100 * time.Millisecond,
			}).(Completion)
			if initialized.Err != nil {
				t.Fatal(initialized.Err)
			}
			done := make(chan Executed, 1)
			failed := make(chan error, 1)
			go func() {
				reply, err := system.AskGrain(ctx, target, Execute{
					CreationID: "creation",
					CommandID:  "cmd-test",
					Line:       "/compact",
				}, 5*time.Second)
				if err != nil {
					failed <- err
					return
				}
				done <- reply.(Executed)
			}()
			select {
			case input := <-started:
				if input.CommandID != "cmd-test" || input.SessionID != "conversation" {
					t.Fatalf("invocation: %#v", input)
				}
			case err := <-failed:
				t.Fatal(err)
			case <-time.After(time.Second):
				t.Fatal("handler not started")
			}
			snapshot := ask(history, sessionactor.Read{}).(sessionactor.View).Snapshot
			if len(snapshot.Events) != 1 || snapshot.Events[0].Type != commands.RunEventName {
				t.Fatal("handler started before run")
			}
			// The handler remains active past the ordinary request deadline.
			select {
			case result := <-done:
				t.Fatalf("handler deadline ended command: %#v", result)
			case failure := <-failed:
				t.Fatal(failure)
			case <-time.After(250 * time.Millisecond):
			}
			if cancel {
				cancelled := ask(target, Cancel{
					CreationID: "creation",
					CommandID:  "cmd-test",
					Cause:      context.Canceled,
				}).(Cancelled)
				if cancelled.Err != nil {
					t.Fatal(cancelled.Err)
				}
				select {
				case cause := <-cancelledSignal:
					if !errors.Is(cause, context.Canceled) {
						t.Fatal(cause)
					}
				case <-time.After(time.Second):
					t.Fatal("handler did not receive cancellation")
				}
			} else {
				ask(handler, releaseHandler{})
			}
			select {
			case result := <-done:
				if cancel && !errors.Is(result.Err, context.Canceled) {
					t.Fatalf("cancel result: %#v", result)
				}
				if !cancel && (result.Err != nil || result.Execution == nil) {
					t.Fatalf("success: %#v", result)
				}
			case err := <-failed:
				t.Fatal(err)
			case <-time.After(time.Second):
				t.Fatal("command did not settle")
			}
			if cancel {
				if result := ask(target, Close{
					CreationID: "creation",
				}).(Completion); result.Err == nil {
					t.Fatal("closed before handler cleanup")
				}
				ask(handler, releaseHandler{})
			}
			ask(handler, releaseHandler{})
			deadline := time.Now().Add(time.Second)
			for {
				result := ask(target, Close{
					CreationID: "creation",
				}).(Completion)
				if result.Err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal(result.Err)
				}
				time.Sleep(time.Millisecond)
			}
			snapshot = ask(history, sessionactor.Read{}).(sessionactor.View).Snapshot
			if len(snapshot.Events) != 2 || snapshot.Events[1].Type != commands.DoneEventName {
				t.Fatalf("lifecycle: %#v", snapshot.Events)
			}
			counts := ask(history, inspectCommits{}).(map[string]int)
			for _, kind := range []string{commands.RunEventName, commands.DoneEventName} {
				expected := 1
				if scenario.loseReply == kind {
					expected = 2
				}
				if counts[kind] != expected {
					t.Fatalf("%s commits: got %d, want %d", kind, counts[kind], expected)
				}
			}
			select {
			case repeated := <-started:
				t.Fatalf("handler repeated after commit retry: %#v", repeated)
			default:
			}

		})
	}
}
