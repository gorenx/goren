package command

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/commands"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/plugin"
)

type commandEngineProviderFixture struct {
	plugin.Base
	backend compaction.Engine
}

func (owner *commandEngineProviderFixture) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "command-engine-provider-fixture",
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[compaction.Engine](owner.backend),
		},
	}
}

func (*commandEngineProviderFixture) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (*commandEngineProviderFixture) Dispose(context.Context) error { return nil }

type commandRegistryProbeFixture struct {
	plugin.Base
	commandPlane commands.Registry
}

func (*commandRegistryProbeFixture) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "command-registry-probe-fixture",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[commands.Registry](),
		},
	}
}

func (owner *commandRegistryProbeFixture) Apply(context.Context) error {
	commandPlane, err := plugin.Require[commands.Registry](owner)
	if err != nil {
		return err
	}
	owner.commandPlane = commandPlane
	return nil
}

func (*commandRegistryProbeFixture) Dispose(context.Context) error { return nil }

type blockingCommandEngineFixture struct {
	commandCompactionStub
	started chan struct{}
	release chan struct{}
}

func (backend *blockingCommandEngineFixture) CompactNow(
	requestContext context.Context,
	subject compaction.ManualAgentContext,
	sourceCommandID *string,
) (*compaction.Result, error) {
	backend.calls = append(backend.calls, commandCompactionCall{
		subject:         subject,
		sourceCommandID: cloneCommandString(sourceCommandID),
	})
	close(backend.started)
	select {
	case <-backend.release:
		return nil, nil
	case <-requestContext.Done():
		return nil, requestContext.Err()
	}
}

func TestPluginUnregistersBeforeDrainingAdmittedCompactCommand(t *testing.T) {
	backend := &blockingCommandEngineFixture{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	commandProvider, err := commands.New(commands.RuntimeOptions{
		InstanceToken: "consumer-lifecycle",
	})
	if err != nil {
		t.Fatal(err)
	}
	consumer := New()
	probe := &commandRegistryProbeFixture{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := runtimeEngine.Start(
		context.Background(),
		commandProvider,
		&commandEngineProviderFixture{
			backend: backend,
		},
		consumer,
		probe,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeContext, cancelClose := context.WithTimeout(
			context.Background(),
			3*time.Second,
		)
		defer cancelClose()
		if err := runtimeEngine.Shutdown(closeContext); err != nil {
			t.Errorf("shutdown command lifecycle fixture: %v", err)
		}
	})

	subject := newCompactAgentFixture(t)
	executeDone := make(chan error, 1)
	go func() {
		_, executeErr := probe.commandPlane.Execute(
			context.Background(),
			subject,
			"/compact",
			commands.ExecuteOptions{},
		)
		executeDone <- executeErr
	}()
	<-backend.started

	unloadDone := make(chan error, 1)
	go func() {
		unloadDone <- runtimeEngine.Unload(context.Background(), handles[2])
	}()
	waitForCommandWithdrawal(t, probe.commandPlane, subject, "compact")
	select {
	case unloadErr := <-unloadDone:
		t.Fatalf("Consumer unloaded before admitted handler drained: %v", unloadErr)
	default:
	}
	close(backend.release)
	if unloadErr := <-unloadDone; unloadErr != nil {
		t.Fatal(unloadErr)
	}
	if executeErr := <-executeDone; executeErr != nil {
		t.Fatal(executeErr)
	}
	if _, found := probe.commandPlane.Find(subject, "compact"); found {
		t.Fatal("/compact returned after Consumer unload")
	}
}

func waitForCommandWithdrawal(
	testingContext *testing.T,
	commandPlane commands.Registry,
	subject *compactAgentFixture,
	nameValue string,
) {
	testingContext.Helper()
	waitContext, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, found := commandPlane.Find(subject, nameValue); !found {
			return
		}
		select {
		case <-ticker.C:
		case <-waitContext.Done():
			testingContext.Fatal(errors.New("command registration was not withdrawn"))
		}
	}
}

var _ compaction.Engine = (*blockingCommandEngineFixture)(nil)
