package compactionactor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/commands"
	commandactor "github.com/gorenx/goren/commands/actor"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/compaction/basic"
	compact "github.com/gorenx/goren/compaction/command"
	meteractor "github.com/gorenx/goren/llm/tokenmeter/actor"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type registrationProbe struct {
	Grain
	ticks int
}
type inspectRegistration struct{}
type registrationStatus struct {
	registered bool
	ticks      int
}

func (owner *registrationProbe) OnReceive(gc *goakt.GrainContext) {
	switch gc.Message().(type) {
	case registerCommand:
		owner.ticks++
	case inspectRegistration:
		gc.Response(registrationStatus{
			registered: owner.command != nil && owner.command.phase == registrationPublished,
			ticks:      owner.ticks,
		})
		return
	}
	owner.Grain.OnReceive(gc)
}

func TestCompactionRegistersAndExecutesItsCommand(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		cancel  bool
		delayed bool
	}{
		{
			name: "success",
		},
		{
			name:   "cancel while flushing",
			cancel: true,
		},
		{
			name:    "registry conflict then retry",
			delayed: true,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			cancel := scenario.cancel
			ctx := context.Background()
			system, err := goakt.NewActorSystem("compaction-test", goakt.WithLogger(log.DiscardLogger))
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
			history, err := session.NewLog("conversation", session.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for range 3 {
				entry, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
					Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock(strings.Repeat("old history ", 1000))},
					Source:  agentmessage.UserMessageSource{},
				})
				if err != nil {
					t.Fatal(err)
				}
				draft, err := session.NewSurfaceEventDraft(session.UserMessageAdded, entry, session.SurfaceIntent{
					Operation: session.SurfaceAppend(),
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := history.Commit(ctx, session.Batch(draft)); err != nil {
					t.Fatal(err)
				}
			}
			conversation, err := goakt.GrainOf[*historyFixture](ctx, system, "conversation", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			flushing := make(chan *goakt.GrainReply, 1)
			if _, err := system.AskGrain(ctx, conversation, bindHistory{
				history:  history,
				flushing: flushing,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			meter, err := goakt.GrainOf[*meteractor.Grain](ctx, system, "conversation", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			calls := make(chan string, 8)
			var provider goakt.Actor = &modelFixture{
				calls: calls,
			}
			router, err := system.Spawn(ctx, "model", provider, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
			if err != nil {
				t.Fatal(err)
			}
			target, err := goakt.GrainOf[*registrationProbe](ctx, system, "conversation", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			providerName, modelName := "summary", "checkpoint"
			admission, err := goakt.GrainOf[*admissionFixture](ctx, system, "admission", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := goakt.GrainOf[*commandactor.Registry](ctx, system, commandactor.RegistryName, goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			if scenario.delayed {
				output, err := system.AskGrain(ctx, catalog, commandactor.Advertise{
					ID:         "blocking-owner",
					SessionID:  "conversation",
					Definition: compact.Definition(),
					Handler:    catalog,
				}, time.Second)
				if err != nil || output.(commandactor.Registered).Err != nil {
					t.Fatalf("block registration: %#v, %v", output, err)
				}
			}
			response, err := system.AskGrain(ctx, target, Initialize{
				Commands: catalog,
				Agent:    admission,
				Config: basic.Config{
					PolicyConfig: basic.PolicyConfig{
						SummarizationProvider: &providerName,
						SummarizationModel:    &modelName,
					},
				},
				AgentID:    "conversation",
				Session:    conversation,
				CreationID: "creation",
				Meter:      meter,
				Router:     router,
				Timeout:    time.Second,
			}, time.Second)
			if err != nil || response.(Completion).Err != nil {
				t.Fatalf("initialize: %#v, %v", response, err)
			}

			ask := func(route *goakt.GrainIdentity, input any) any {
				t.Helper()
				output, err := system.AskGrain(ctx, route, input, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				return output
			}
			executor, err := goakt.GrainOf[*commandactor.Grain](ctx, system, "conversation", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			if result := ask(executor, commandactor.Initialize{
				CreationID: "creation",
				Session:    conversation,
				Registry:   catalog,
				Timeout:    time.Second,
			}).(commandactor.Completion); result.Err != nil {
				t.Fatal(result.Err)
			}
			if scenario.delayed {
				time.Sleep(1100 * time.Millisecond)
				ask(catalog, commandactor.Unregister{
					ID: "blocking-owner",
				})
			}
			registrationDeadline := time.Now().Add(3 * time.Second)
			for {
				resolved := ask(catalog, commandactor.Resolve{
					SessionID: "conversation",
					Name:      "compact",
				}).(commandactor.Resolved)
				if resolved.Found && resolved.Handler.Equal(target) {
					break
				}
				if time.Now().After(registrationDeadline) {
					t.Fatal("automatic command registration failed")
				}
				time.Sleep(time.Millisecond)
			}
			for {
				status := ask(target, inspectRegistration{}).(registrationStatus)
				if status.registered {
					if scenario.delayed {
						if status.ticks == 0 {
							t.Fatal("registration did not retry")
						}
						time.Sleep(1100 * time.Millisecond)
						if later := ask(target, inspectRegistration{}).(registrationStatus); later.ticks != status.ticks {
							t.Fatal("registration timer continued after acknowledgement")
						}
					}
					break
				}
				if time.Now().After(registrationDeadline) {
					t.Fatal("registration acknowledgement was not consumed")
				}
				time.Sleep(time.Millisecond)
			}
			registered := ask(catalog, commandactor.Resolve{
				SessionID: "conversation",
				Name:      "compact",
			}).(commandactor.Resolved)
			if !registered.Found || registered.Handler.String() != target.String() {
				t.Fatal("Compaction did not register itself")
			}
			stale := ask(target, commandactor.Invoke{
				CreationID: "old-creation",
				Input: commands.Invocation{
					SessionID: "conversation",
					CommandID: "old-command",
				},
			}).(commandactor.Completed)
			if stale.Err == nil {
				t.Fatal("accepted retired Agent invocation")
			}
			invalid := ask(executor, commandactor.Execute{
				CreationID: "creation",
				CommandID:  "invalid-command",
				Line:       "/compact unexpected",
			}).(commandactor.Executed)
			if invalid.Err != nil || invalid.Execution == nil || invalid.Execution.Result.Kind != commands.ResultError {
				t.Fatalf("invalid arguments: %#v", invalid)
			}
			select {
			case call := <-calls:
				t.Fatalf("invalid arguments started model: %s", call)
			default:
			}
			done := make(chan commandactor.Executed, 1)
			failures := make(chan error, 1)
			go func() {
				output, err := system.AskGrain(ctx, executor, commandactor.Execute{
					CreationID: "creation",
					CommandID:  "compact-test",
					Line:       "/compact",
				}, 5*time.Second)
				if err != nil {
					failures <- err
					return
				}
				done <- output.(commandactor.Executed)
			}()
			var flush *goakt.GrainReply
			select {
			case flush = <-flushing:
			case err := <-failures:
				t.Fatal(err)
			case output := <-done:
				t.Fatalf("command returned before flush: %#v", output)
			case <-time.After(3 * time.Second):
				t.Fatal("compaction did not reach Flush")
			}
			snapshot := ask(conversation, sessionactor.Read{}).(sessionactor.View).Snapshot
			foundOrigin := false
			for _, entry := range snapshot.Events {
				if entry.Type == compaction.StartEventName {
					event, err := compaction.DecodeStart(entry.Data)
					if err != nil {
						t.Fatal(err)
					}
					if event.SourceCommandID == nil || *event.SourceCommandID != "compact-test" {
						t.Fatalf("command source: %#v", event)
					}
					foundOrigin = true
				}
			}
			if !foundOrigin {
				t.Fatal("missing compaction start")
			}
			if cancel {
				if result := ask(executor, commandactor.Cancel{
					CreationID: "creation",
					CommandID:  "compact-test",
					Cause:      context.Canceled,
				}).(commandactor.Cancelled); result.Err != nil {
					t.Fatal(result.Err)
				}
				select {
				case output := <-done:
					if !errors.Is(output.Err, context.Canceled) {
						t.Fatalf("cancellation: %#v", output)
					}
				case err := <-failures:
					t.Fatal(err)
				case <-time.After(time.Second):
					t.Fatal("UI cancellation waited for Flush")
				}
			}
			if result := ask(target, Close{}).(Completion); result.Err == nil {
				t.Fatal("closed active or registered Compaction")
			}
			flush.Response(sessionactor.Flushed{
				Barrier: snapshot.Barrier,
			})
			if !cancel {
				select {
				case output := <-done:
					if output.Err != nil || output.Execution == nil || output.Execution.Result.SourceEventSeq == nil {
						t.Fatalf("command result: %#v", output)
					}
				case err := <-failures:
					t.Fatal(err)
				case <-time.After(time.Second):
					t.Fatal("command not completed")
				}
			}
			deadline := time.Now().Add(time.Second)
			for {
				if result := ask(target, Close{}).(Completion); result.Err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("compaction did not settle")
				}
				time.Sleep(time.Millisecond)
			}
			if result := ask(catalog, commandactor.Resolve{
				SessionID: "conversation",
				Name:      "compact",
			}).(commandactor.Resolved); result.Found {
				t.Fatal("command was not withdrawn")
			}
			if held := ask(admission, inspectMaintenance{}); held != "" {
				t.Fatalf("maintenance retained: %v", held)
			}
			if result := ask(target, Close{}).(Completion); result.Err != nil {
				t.Fatal(result.Err)
			}
			for {
				if result := ask(executor, commandactor.Close{
					CreationID: "creation",
				}).(commandactor.Completion); result.Err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("Commands did not release invocation")
				}
				time.Sleep(time.Millisecond)
			}
			snapshot = ask(conversation, sessionactor.Read{}).(sessionactor.View).Snapshot
			run, doneCount := 0, 0
			for _, entry := range snapshot.Events {
				if entry.Type == commands.RunEventName {
					run++
				}
				if entry.Type == commands.DoneEventName {
					doneCount++
				}
			}
			if run != 2 || doneCount != 2 {
				t.Fatalf("command facts: run=%d done=%d", run, doneCount)
			}
		})
	}
}
