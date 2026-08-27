// Package oneshot owns the complete terminal Subagent use case.
package oneshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
	"github.com/gorenx/goren/subagent/internal/lineage"
	"github.com/gorenx/goren/tools"
)

// SeedBuilders resolves the exact registered child seed strategy.
type SeedBuilders interface {
	Find(string) (subagent.SeedBuilder, bool)
}

// ChildEnvironmentOptions describes the child-local behavior required by one
// OneShot execution without exposing its Plugin implementation.
type ChildEnvironmentOptions struct {
	Descriptor   subagent.OneShotDescriptor
	Persona      *string
	ToolFilter   *tools.ToolRestriction
	OutputSchema json.RawMessage
}

// ChildEnvironment installs child-local behavior and exposes the optional
// structured result captured by that exact environment.
type ChildEnvironment interface {
	agent.Provisioner
	StructuredOutput() (json.RawMessage, bool)
}

// EnvironmentBuilder creates the child-local environment consumed by OneShot.
type EnvironmentBuilder interface {
	Build(ChildEnvironmentOptions) ChildEnvironment
}

// Dependencies contains the capabilities required by OneShot execution.
type Dependencies struct {
	Agents       agent.Registry
	Constructor  agent.Constructor
	SeedBuilders SeedBuilders
	Environments EnvironmentBuilder
	Publisher    sharedexecution.EventPublisher
	Executions   *sharedexecution.Registry
}

// Service starts and observes terminal child executions.
type Service struct {
	dependencies Dependencies
}

// Mode identifies the business mode implemented by Service.
func (*Service) Mode() subagent.Mode {
	return subagent.ModeOneShot
}

// New constructs the OneShot application service.
func New(dependencySet Dependencies) (*Service, error) {
	if dependencySet.Agents == nil || dependencySet.Constructor == nil ||
		dependencySet.SeedBuilders == nil ||
		dependencySet.Environments == nil ||
		dependencySet.Executions == nil {
		return nil, errors.New(
			"subagent: OneShot requires Agent Registry, Constructor, " +
				"SeedBuilders, Environment Builder, and Execution Registry",
		)
	}
	return &Service{
		dependencies: dependencySet,
	}, nil
}

// Interrupt stops the exact live OneShot execution, if it still exists.
// Authorization is enforced by the parent Subagent Service before dispatch.
func (owner *Service) Interrupt(
	requestContext context.Context,
	childID session.SessionID,
) error {
	if requestContext == nil {
		return errors.New("subagent: OneShot Interrupt context is nil")
	}
	if requestErr := requestContext.Err(); requestErr != nil {
		return requestErr
	}
	entry, found := owner.dependencies.Executions.Find(childID)
	if !found || entry.Mode != subagent.ModeOneShot {
		return nil
	}
	entry.Subject.Cancel(
		agent.ParentCancel{},
		agent.CancelOptions{
			KeepInbox: false,
		},
	)
	entry.Execution.Stop(sharedexecution.StopInterrupted)
	return nil
}

// Close requests every live OneShot execution to close and waits only until
// each exact Agent enters Closing. Agent owns structural Scope teardown.
func (owner *Service) Close(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	targets := make([]sharedexecution.Entry, 0)
	for _, entry := range owner.dependencies.Executions.List() {
		if entry.Mode != subagent.ModeOneShot {
			continue
		}
		targets = append(targets, entry)
		entry.Execution.Stop(sharedexecution.StopModule)
	}
	return waitForClosing(closeContext, targets)
}

func waitForClosing(
	closeContext context.Context,
	targets []sharedexecution.Entry,
) error {
	for _, entry := range targets {
		select {
		case <-entry.Closing:
		case <-closeContext.Done():
			return context.Cause(closeContext)
		}
	}
	return nil
}

// Start creates one child, accepts its initial message, and returns the common
// Execution. The Start Context is not retained after publication.
func (owner *Service) Start(
	ctx context.Context,
	command subagent.OneShotStartCommand,
) (subagent.Execution, error) {
	if ctx == nil {
		return nil, errors.New("subagent: OneShot Start context is nil")
	}
	if requestErr := ctx.Err(); requestErr != nil {
		return nil, requestErr
	}
	requestSnapshot, snapshotErr := command.Request()
	if snapshotErr != nil {
		return nil, snapshotErr
	}
	if requestSnapshot.Parent == nil ||
		!owner.dependencies.Agents.Contains(requestSnapshot.Parent) {
		return nil, &subagent.Error{
			Code:    subagent.ErrorUnauthorized,
			Message: "OneShot Start requires the exact live parent Agent",
		}
	}
	seedBuilderName := command.SeedBuilderName()
	if len(requestSnapshot.OutputSchema) != 0 {
		requestSnapshot.OutputSchema, snapshotErr = tools.SnapshotObjectSchema(
			requestSnapshot.OutputSchema,
		)
		if snapshotErr != nil {
			return nil, snapshotErr
		}
	}
	childID, identityErr := sharedexecution.NewChildID()
	if identityErr != nil {
		return nil, identityErr
	}
	runID, identityErr := sharedexecution.NewRunID()
	if identityErr != nil {
		return nil, identityErr
	}
	seed, seedErr := owner.buildSeed(
		ctx,
		seedBuilderName,
		childID,
		requestSnapshot.Parent,
	)
	if seedErr != nil {
		return nil, seedErr
	}
	childLineage, lineageErr := lineage.From(
		requestSnapshot.Parent,
		requestSnapshot.MaxDepth,
	)
	if lineageErr != nil {
		return nil, lineageErr
	}
	environment := owner.dependencies.Environments.Build(
		ChildEnvironmentOptions{
			Descriptor: subagent.OneShotDescriptor{
				Provider: seedBuilderName,
				Label:    command.Label(),
			},
			Persona:      requestSnapshot.Persona,
			ToolFilter:   requestSnapshot.ToolFilter,
			OutputSchema: requestSnapshot.OutputSchema,
		},
	)
	if environment == nil {
		return nil, errors.New(
			"subagent: OneShot Environment Builder returned nil",
		)
	}
	initiatedContext, contextErr := agent.WithInitiator(
		ctx,
		requestSnapshot.Parent,
	)
	if contextErr != nil {
		return nil, contextErr
	}
	handle, createErr := owner.dependencies.Constructor.Create(
		initiatedContext,
		agent.CreateOptions{
			SessionID:     childID,
			Metadata:      childLineage.Metadata(int64(len(seed))),
			Seed:          seed,
			AgentOptions:  childLineage.AgentOptions(requestSnapshot.AgentOptions),
			Provisioner:   environment,
			RuntimeParent: requestSnapshot.Parent,
		},
	)
	if createErr != nil {
		return nil, createErr
	}
	prompt, messageErr := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: requestSnapshot.Prompt,
		Source: agentmessage.UserMessageSource{
			Kind: "user",
		},
	})
	if messageErr != nil {
		return nil, errors.Join(
			messageErr,
			handle.Dispose(context.WithoutCancel(ctx)),
		)
	}
	if followErr := handle.Subject.Followup(prompt); followErr != nil {
		return nil, errors.Join(
			followErr,
			handle.Dispose(context.WithoutCancel(ctx)),
		)
	}
	terminator := &executionTerminator{
		handle:      handle,
		parent:      requestSnapshot.Parent,
		seedBuilder: seedBuilderName,
		runID:       runID,
		boundary:    int64(len(seed)),
		environment: environment,
		publisher:   owner.dependencies.Publisher,
	}
	running, executionErr := sharedexecution.New(runID, childID, terminator)
	if executionErr != nil {
		return nil, errors.Join(
			executionErr,
			handle.Dispose(context.WithoutCancel(ctx)),
		)
	}
	if activationErr := running.Activate(); activationErr != nil {
		return nil, errors.Join(
			activationErr,
			handle.Dispose(context.WithoutCancel(ctx)),
		)
	}
	terminator.running = running
	terminator.executions = owner.dependencies.Executions
	if publishErr := owner.dependencies.Executions.Publish(
		sharedexecution.Entry{
			Execution: running,
			Mode:      subagent.ModeOneShot,
			Parent:    requestSnapshot.Parent,
			Subject:   handle.Subject,
			Closing:   handle.ClosingSignal(),
		},
	); publishErr != nil {
		return nil, errors.Join(
			publishErr,
			handle.Dispose(context.WithoutCancel(ctx)),
		)
	}
	if owner.dependencies.Publisher != nil {
		owner.dependencies.Publisher.PublishStarted(
			requestSnapshot.Parent,
			subagent.Started{
				RunID:    runID,
				Provider: seedBuilderName,
				ID:       childID,
				Local:    true,
			},
		)
	}
	go watch(running, handle)
	return running, nil
}

func (owner *Service) buildSeed(
	requestContext context.Context,
	name string,
	childID session.SessionID,
	parentAgent agent.Agent,
) ([]session.Event, error) {
	builder, found := owner.dependencies.SeedBuilders.Find(name)
	if !found {
		return nil, &subagent.Error{
			Code: subagent.ErrorNoSeedBuilder,
			Message: fmt.Sprintf(
				"no subagent SeedBuilder registered for %q",
				name,
			),
		}
	}
	parentSession := parentAgent.SessionValue()
	if parentSession == nil {
		return nil, errors.New("subagent: parent Session is unavailable")
	}
	seedValue, seedErr := builder.BuildSeed(
		requestContext,
		parentSession.Events(),
	)
	if seedErr != nil {
		return nil, seedErr
	}
	return seedValue.EventPrefix(), nil
}

func watch(running *sharedexecution.Execution, handle agent.Handle) {
	idleResult := make(chan error, 1)
	go func() {
		idleResult <- handle.Subject.WhenIdle(context.Background())
	}()
	select {
	case <-handle.ClosingSignal():
		running.Stop(sharedexecution.StopExternal)
	case <-idleResult:
		running.Stop(sharedexecution.StopNormal)
	}
}
