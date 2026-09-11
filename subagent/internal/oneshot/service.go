// Package oneshot owns the complete terminal Subagent use case.
package oneshot

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/agentclient"
	"github.com/gorenx/goren/subagent/internal/childpolicy"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
	"github.com/gorenx/goren/subagent/internal/lineage"
	"github.com/gorenx/goren/subagent/internal/seedbuilder"
	"github.com/gorenx/goren/tools"
)

type SeedBuilders interface {
	Find(string) (subagent.SeedBuilder, bool)
}

type Agents interface {
	Find(context.Context, session.SessionID) (agentclient.Agent, error)
	CreateChild(context.Context, agentregistry.CreateChildAgent) (agentclient.Agent, error)
}

type Dependencies struct {
	Agents       Agents
	SeedBuilders SeedBuilders
	Extensions   subagent.Extension
	Publisher    sharedexecution.EventPublisher
	Executions   *sharedexecution.Registry
}

type Service struct {
	dependencies Dependencies
}

func (*Service) Mode() subagent.Mode { return subagent.ModeOneShot }

func New(dependencySet Dependencies) (*Service, error) {
	if dependencySet.Agents == nil || dependencySet.SeedBuilders == nil || dependencySet.Executions == nil {
		return nil, errors.New("subagent: OneShot requires Agent directory, SeedBuilders, and Execution Registry")
	}
	return &Service{dependencies: dependencySet}, nil
}

func (owner *Service) Interrupt(ctx context.Context, childID session.SessionID) error {
	if ctx == nil {
		return errors.New("subagent: OneShot Interrupt context is nil")
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	entry, found := owner.dependencies.Executions.Find(childID)
	if !found || entry.Mode != subagent.ModeOneShot {
		return nil
	}
	child, err := owner.dependencies.Agents.Find(ctx, childID)
	if err != nil {
		return err
	}
	if err := child.Cancel(ctx, agent.ParentCancel{}, agent.CancelOptions{KeepInbox: false}); err != nil {
		return err
	}
	entry.Execution.Stop(sharedexecution.CloseInterrupted)
	return nil
}

func (owner *Service) Close(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	var targets []sharedexecution.ManagedExecution
	for _, entry := range owner.dependencies.Executions.List() {
		if entry.Mode == subagent.ModeOneShot {
			targets = append(targets, entry.Execution)
			entry.Execution.Stop(sharedexecution.CloseModule)
		}
	}
	for _, target := range targets {
		if err := target.Wait(closeContext); err != nil {
			return err
		}
	}
	return nil
}

func (owner *Service) Start(ctx context.Context, command subagent.OneShotStartCommand) (subagent.Execution, error) {
	if ctx == nil {
		return nil, errors.New("subagent: OneShot Start context is nil")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	request, err := command.Snapshot()
	if err != nil {
		return nil, err
	}
	parent, err := owner.dependencies.Agents.Find(ctx, request.ParentID)
	if err != nil {
		return nil, &subagent.Error{Code: subagent.ErrorUnauthorized, Message: "OneShot Start requires the exact live parent Agent", Cause: err}
	}
	parentView, err := parent.Read(ctx)
	if err != nil {
		return nil, err
	}
	if len(request.OutputSchema) != 0 {
		request.OutputSchema, err = tools.SnapshotObjectSchema(request.OutputSchema)
		if err != nil {
			return nil, err
		}
	}
	childID, err := sharedexecution.NewChildID()
	if err != nil {
		return nil, err
	}
	runID, err := sharedexecution.NewRunID()
	if err != nil {
		return nil, err
	}
	seed, err := owner.buildSeed(ctx, command.SeedBuilderName(), parentView.Snapshot.Events)
	if err != nil {
		return nil, err
	}
	seed, err = childpolicy.AppendDelegation(seed)
	if err != nil {
		return nil, err
	}
	descriptor := subagent.OneShotDescriptor{Provider: command.SeedBuilderName(), Label: command.Label()}
	seed, err = seedbuilder.AppendDescriptor(childID, seed, descriptor)
	if err != nil {
		return nil, err
	}
	childLineage, err := lineage.From(request.ParentID, parentView.Header, agent.Options{}, request.MaxDepth)
	if err != nil {
		return nil, err
	}
	configuration, structured, err := owner.configure(ctx, request)
	if err != nil {
		return nil, err
	}
	childCreation := agentregistry.Creation{AgentID: childID, CreationID: string(runID)}
	child, err := owner.dependencies.Agents.CreateChild(ctx, agentregistry.CreateChildAgent{
		Parent:         parent.Creation(),
		Child:          childCreation,
		Options:        session.CreateOptions{Seed: seed, Metadata: childLineage.Metadata(int64(len(seed)))},
		AgentOptions:   request.AgentOptions,
		Tools:          &configuration.Tools,
		PromptSections: configuration.PromptSections,
	})
	if err != nil {
		return nil, err
	}
	dispose := func(failure error) (subagent.Execution, error) {
		return nil, errors.Join(failure, child.Close(context.WithoutCancel(ctx)))
	}
	prompt, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{Content: request.Prompt, Source: agentmessage.UserMessageSource{}})
	if err != nil {
		return dispose(err)
	}
	if err = child.Followup(ctx, prompt); err != nil {
		return dispose(err)
	}
	running, err := newOneShotExecution(runID, childID)
	if err != nil {
		return dispose(err)
	}
	running.executions = owner.dependencies.Executions
	running.child = child
	running.parent = parent.Creation()
	running.seedBuilder = command.SeedBuilderName()
	running.boundary = int64(len(seed))
	running.structured = structured
	running.publisher = owner.dependencies.Publisher
	if err = sharedexecution.Publish(owner.dependencies.Executions, owner.dependencies.Publisher, sharedexecution.Entry{
		Execution: running,
		Mode:      subagent.ModeOneShot,
		Parent:    parent.Creation(),
		Subject:   child.Creation(),
	}, command.SeedBuilderName()); err != nil {
		return dispose(err)
	}
	go watch(running, child)
	return running, nil
}

func (owner *Service) buildSeed(ctx context.Context, name string, events []session.Event) ([]session.Event, error) {
	builder, found := owner.dependencies.SeedBuilders.Find(name)
	if !found {
		return nil, &subagent.Error{Code: subagent.ErrorNoSeedBuilder, Message: fmt.Sprintf("no subagent SeedBuilder registered for %q", name)}
	}
	seedValue, err := builder.BuildSeed(ctx, events)
	if err != nil {
		return nil, err
	}
	return seedValue.EventPrefix(), nil
}

func watch(running *oneShotExecution, child agentclient.Agent) {
	_ = child.WhenIdle(context.Background())
	running.Stop(sharedexecution.CloseNormal)
}
