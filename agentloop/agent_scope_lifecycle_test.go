package agentloop

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

type blockingScopeRuntime struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (runtimeState *blockingScopeRuntime) Dispatch(
	context.Context,
	agent.AgentEvent,
) error {
	runtimeState.once.Do(func() {
		close(runtimeState.entered)
	})
	<-runtimeState.release
	return nil
}

func (*blockingScopeRuntime) ResolvePreStep(
	requestContext context.Context,
	notice agent.PreStepNotice,
	terminal agent.PreStepAction,
) (agent.PreStepDecision, error) {
	return terminal.Execute(requestContext, notice)
}

func (*blockingScopeRuntime) ResolveRequest(
	requestContext context.Context,
	notice agent.RequestNotice,
	terminal agent.RequestAction,
) (agent.RequestResolution, error) {
	return terminal.Execute(requestContext, notice)
}

func (*blockingScopeRuntime) ResolveRequestError(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
	terminal agent.RequestErrorHandler,
) (agent.RequestErrorAction, error) {
	return terminal.Execute(requestContext, notice)
}

func TestAgentScopeCloseDrainsAdmittedDispatch(t *testing.T) {
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		t.Fatal(err)
	}
	toolSettings, err := tools.ValidateConfig(tools.Config{})
	if err != nil {
		t.Fatal(err)
	}
	promptService := systemprompt.New(
		promptSettings,
		systemprompt.RegistryOptions{},
	)
	toolService := tools.New(toolSettings)
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err = runtimeEngine.Start(
		context.Background(),
		promptService,
		toolService,
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if shutdownErr := runtimeEngine.Shutdown(context.Background()); shutdownErr != nil {
			t.Error(shutdownErr)
		}
	})
	promptLayer := promptService.NewLayer()
	toolLayer, err := toolService.NewLayer(
		context.Background(),
		promptLayer,
	)
	if err != nil {
		t.Fatal(err)
	}
	runtimeState := &blockingScopeRuntime{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	scopeState, err := newAgentScope(
		runtimeState,
		runtimeState,
		promptLayer,
		toolLayer,
	)
	if err != nil {
		t.Fatal(err)
	}
	dispatchDone := make(chan error, 1)
	go func() {
		dispatchDone <- scopeState.Dispatch(
			context.Background(),
			agent.Created{},
		)
	}()
	<-runtimeState.entered
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- scopeState.Close(context.Background())
	}()
	select {
	case closeErr := <-closeDone:
		t.Fatalf("Scope closed before admitted dispatch drained: %v", closeErr)
	case <-time.After(20 * time.Millisecond):
	}
	close(runtimeState.release)
	if err = <-dispatchDone; err != nil {
		t.Fatal(err)
	}
	if err = <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err = scopeState.Dispatch(context.Background(), agent.Created{}); err == nil || !strings.Contains(err.Error(), "Scope is closed") {
		t.Fatalf("post-close Dispatch error = %v", err)
	}
}

var _ agentEvents = (*blockingScopeRuntime)(nil)
var _ agentWaterfalls = (*blockingScopeRuntime)(nil)
