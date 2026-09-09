package toolactor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/toolaskuser"
	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/tools/tool"
	toolactor "github.com/gorenx/goren/tools/tool/actor"
	"github.com/gorenx/goren/userquestions"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type waitingQuestions struct {
	entered   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
}

func (service *waitingQuestions) Ask(ctx context.Context, _ userquestions.Request) (userquestions.Answer, error) {
	service.entered <- struct{}{}
	<-ctx.Done()
	service.cancelled <- struct{}{}
	<-service.release
	return userquestions.Answer{}, context.Cause(ctx)
}

func installTool(t *testing.T, source tool.Provider) (goakt.ActorSystem, *goakt.GrainIdentity) {
	t.Helper()
	system, directory, target := startTool(t)
	provisionTool(t, system, directory, target, source)
	return system, target
}

func provisionTool(t *testing.T, system goakt.ActorSystem, directory, target *goakt.GrainIdentity, source tool.Provider) {
	t.Helper()
	reply := ask[toolactor.Completion](t, system, target, toolactor.Provision{
		CreationID: "creation",
		Revision:   "initial",
		Provider:   source,
		Schema:     questionSchema(t),
	})
	if reply.Err != nil {
		t.Fatal(reply.Err)
	}
	reply = ask[toolactor.Completion](t, system, target, toolactor.Commit{
		CreationID: "creation",
		Revision:   "initial",
	})
	if reply.Err != nil {
		t.Fatal(reply.Err)
	}
}

func invocation(identity tool.Identity) toolactor.Execute {
	return toolactor.Execute{
		CreationID:     "creation",
		Implementation: identity,
		Invocation: tools.Invocation{
			CallID:    "call",
			SessionID: "conversation",
			Name:      toolaskuser.Name,
			Arguments: []byte(`{"questions":[{"id":"q","question":"Continue?"}]}`),
		},
	}
}

func TestCancelWaitsForBodyExitBeforeAllowingResourceClose(t *testing.T) {
	service := &waitingQuestions{
		entered:   make(chan struct{}, 1),
		cancelled: make(chan struct{}, 1),
		release:   make(chan struct{}),
	}
	source := &provider{
		questions: service,
	}
	system, target := installTool(t, source)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		select {
		case <-service.release:
		default:
			close(service.release)
		}
	})
	input := invocation(currentIdentity(t, system, target))
	input.Context = ctx
	results := make(chan tools.BodyResult, 1)
	go func() {
		payload, err := system.AskGrain(context.Background(), target, input, 3*time.Second)
		if err != nil {
			results <- tools.BodyResult{
				Err: err,
			}
			return
		}
		result, ok := payload.(tools.BodyResult)
		if !ok {
			result.Err = errors.New("unexpected body reply")
		}
		results <- result
	}()
	select {
	case <-service.entered:
	case <-time.After(time.Second):
		t.Fatal("body did not start")
	}
	if result := ask[tools.BodyResult](t, system, target, input); result.Err == nil {
		t.Fatal("duplicate in-flight call was dispatched")
	}
	if result := ask[toolactor.Completion](t, system, target, toolactor.CancelCall{
		CreationID: "creation",
		CallID:     "call",
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	select {
	case <-service.cancelled:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach the body")
	}
	select {
	case <-results:
		t.Fatal("cancellation acknowledgement was treated as body completion")
	default:
	}
	if result := ask[toolactor.Completion](t, system, target, toolactor.Close{
		CreationID: "creation",
	}); result.Err == nil || source.closes.Load() != 0 {
		t.Fatal("resource cleanup overlapped the active body")
	}
	close(service.release)
	select {
	case result := <-results:
		if !errors.Is(result.Err, context.Canceled) {
			t.Fatalf("body cancellation lost: %v", result.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("body completion was not delivered")
	}
	if result := ask[toolactor.Completion](t, system, target, toolactor.Close{
		CreationID: "creation",
	}); result.Err != nil || source.closes.Load() != 1 {
		t.Fatalf("close after body exit failed: %v", result.Err)
	}
}

func TestExecutionRejectsWrongIdentityRevisionAndClosedTool(t *testing.T) {
	system, target := installTool(t, &provider{})
	for _, test := range []struct {
		name   string
		change func(*toolactor.Execute)
	}{
		{
			name: "creation",
			change: func(input *toolactor.Execute) {
				input.CreationID = "previous"
			},
		},
		{
			name: "session",
			change: func(input *toolactor.Execute) {
				input.Invocation.SessionID = "another"
			},
		},
		{
			name: "tool",
			change: func(input *toolactor.Execute) {
				input.Invocation.Name = "another"
			},
		},
		{
			name: "implementation",
			change: func(input *toolactor.Execute) {
				input.Implementation = "different-update"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := invocation(currentIdentity(t, system, target))
			test.change(&input)
			if result := ask[tools.BodyResult](t, system, target, input); result.Err == nil {
				t.Fatal("invalid execution accepted")
			}
		})
	}
	retained := invocation(currentIdentity(t, system, target))
	if result := ask[tools.BodyResult](t, system, target, retained); result.Err != nil {
		t.Fatalf("valid execution failed: %v", result.Err)
	}
	if result := ask[toolactor.Completion](t, system, target, toolactor.Close{
		CreationID: "creation",
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	if result := ask[tools.BodyResult](t, system, target, retained); result.Err == nil {
		t.Fatal("closed tool remained executable")
	}
}

func TestCancellationTargetsOnlyOneConcurrentCall(t *testing.T) {
	service := &waitingQuestions{
		entered:   make(chan struct{}, 2),
		cancelled: make(chan struct{}, 2),
		release:   make(chan struct{}),
	}
	system, target := installTool(t, &provider{
		questions: service,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		select {
		case <-service.release:
		default:
			close(service.release)
		}
	})
	type outcome struct {
		id  agentmessage.CallID
		err error
	}
	results := make(chan outcome, 2)
	for _, identifier := range []agentmessage.CallID{"first", "second"} {
		go func() {
			input := invocation(currentIdentity(t, system, target))
			input.Invocation.CallID = identifier
			input.Context = ctx
			payload, err := system.AskGrain(context.Background(), target, input, 3*time.Second)
			if err == nil {
				if reply, ok := payload.(tools.BodyResult); ok {
					err = reply.Err
				} else {
					err = errors.New("invalid body reply")
				}
			}
			results <- outcome{
				id:  identifier,
				err: err,
			}
		}()
	}
	for range 2 {
		select {
		case <-service.entered:
		case <-time.After(time.Second):
			t.Fatal("distinct calls did not both enter the body")
		}
	}
	if reply := ask[toolactor.Completion](t, system, target, toolactor.CancelCall{
		CreationID: "creation",
		CallID:     "first",
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	select {
	case <-service.cancelled:
	case <-time.After(time.Second):
		t.Fatal("first cancellation was not delivered")
	}
	close(service.release)
	select {
	case result := <-results:
		if result.id != "first" || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("wrong call settled: %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("first call did not settle")
	}
	if reply := ask[toolactor.Completion](t, system, target, toolactor.Close{
		CreationID: "creation",
	}); reply.Err == nil {
		t.Fatal("resource cleanup started before the second body exited")
	}
	if reply := ask[toolactor.Completion](t, system, target, toolactor.CancelCall{
		CreationID: "creation",
		CallID:     "second",
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	select {
	case result := <-results:
		if result.id != "second" || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("second call result: %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("second call did not settle")
	}
	if reply := ask[toolactor.Completion](t, system, target, toolactor.Close{
		CreationID: "creation",
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
}

func currentIdentity(t *testing.T, system goakt.ActorSystem, target *goakt.GrainIdentity) tool.Identity {
	t.Helper()
	description := ask[toolactor.Description](t, system, target, toolactor.Describe{CreationID: "creation"})
	if description.Err != nil || description.Implementation == "" {
		t.Fatalf("tool description: %+v", description)
	}
	return description.Implementation
}
