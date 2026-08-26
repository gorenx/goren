// Package oneshot owns the complete terminal Subagent use case.
package oneshot

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/childrequest"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
	"github.com/gorenx/goren/subagent/internal/lineage"
	"github.com/gorenx/goren/tools"
)

// SeedBuilders resolves the exact registered child seed strategy.
type SeedBuilders interface {
	Find(string) (subagent.SeedBuilder, bool)
}

// Lifecycle publishes paired Subagent execution facts.
type Lifecycle interface {
	Started(agent.Agent, subagent.Started)
	Ended(agent.Agent, subagent.Ended)
}

// Dependencies contains the capabilities required by OneShot execution.
type Dependencies struct {
	Agents       agent.Registry
	Constructor  agent.Constructor
	Approval     approval.DelegationPolicy
	SeedBuilders SeedBuilders
	Lifecycle    Lifecycle
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
		dependencySet.Executions == nil {
		return nil, errors.New(
			"subagent: OneShot requires Agent Registry, Constructor, " +
				"SeedBuilders, and Execution Registry",
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
	requestContext context.Context,
	command subagent.StartCommand,
) (subagent.Execution, error) {
	if requestContext == nil {
		return nil, errors.New("subagent: OneShot Start context is nil")
	}
	if requestErr := requestContext.Err(); requestErr != nil {
		return nil, requestErr
	}
	if command.Mode() != subagent.ModeOneShot {
		return nil, errors.New("subagent: OneShot received another start mode")
	}
	requestSnapshot, snapshotErr := childrequest.Snapshot(command.Request())
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
		requestContext,
		command.SeedBuilderName(),
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
	descriptor := newDescriptorAppender(
		subagent.OneShotDescriptor{
			Provider: command.SeedBuilderName(),
			Label:    command.Label(),
		},
	)
	childPlugins := []plugin.Plugin{
		descriptor,
	}
	var structured *structuredCapture
	if len(requestSnapshot.OutputSchema) != 0 {
		structured = newStructuredCapture(requestSnapshot.OutputSchema)
		childPlugins = append(childPlugins, structured)
	}
	initiatedContext, contextErr := agent.WithInitiator(
		requestContext,
		requestSnapshot.Parent,
	)
	if contextErr != nil {
		return nil, contextErr
	}
	handle, createErr := owner.dependencies.Constructor.Create(
		initiatedContext,
		agent.CreateOptions{
			SessionID:    childID,
			Metadata:     childLineage.Metadata(int64(len(seed))),
			Seed:         seed,
			AgentOptions: childLineage.AgentOptions(requestSnapshot.AgentOptions),
			Provisioner: owner.provisioner(
				scopePolicy{
					persona:     requestSnapshot.Persona,
					restriction: requestSnapshot.ToolFilter,
					plugins:     childPlugins,
				},
			),
			RuntimeParent: requestSnapshot.Parent,
		},
	)
	if createErr != nil {
		return nil, createErr
	}
	prompt, messageErr := llm.NewUserMessage(llm.UserMessageInput{
		Content: requestSnapshot.Prompt,
		Source: llm.UserMessageSource{
			Kind: "user",
		},
	})
	if messageErr != nil {
		return nil, errors.Join(
			messageErr,
			handle.Dispose(context.WithoutCancel(requestContext)),
		)
	}
	if followErr := handle.Subject.Followup(prompt); followErr != nil {
		return nil, errors.Join(
			followErr,
			handle.Dispose(context.WithoutCancel(requestContext)),
		)
	}
	terminator := &executionTerminator{
		handle:      handle,
		parent:      requestSnapshot.Parent,
		seedBuilder: command.SeedBuilderName(),
		runID:       runID,
		boundary:    int64(len(seed)),
		structured:  structured,
		lifecycle:   owner.dependencies.Lifecycle,
	}
	running, executionErr := sharedexecution.New(runID, childID, terminator)
	if executionErr != nil {
		return nil, errors.Join(
			executionErr,
			handle.Dispose(context.WithoutCancel(requestContext)),
		)
	}
	if activationErr := running.Activate(); activationErr != nil {
		return nil, errors.Join(
			activationErr,
			handle.Dispose(context.WithoutCancel(requestContext)),
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
			handle.Dispose(context.WithoutCancel(requestContext)),
		)
	}
	terminator.published = true
	if owner.dependencies.Lifecycle != nil {
		owner.dependencies.Lifecycle.Started(
			requestSnapshot.Parent,
			subagent.Started{
				RunID:    runID,
				Provider: command.SeedBuilderName(),
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
	parentHeader := parentSession.Header()
	seedValue, seedErr := builder.BuildSeed(
		requestContext,
		subagent.SeedRequest{
			ChildID: childID,
			Parent: subagent.ParentSnapshot{
				SessionID: parentHeader.ID,
				Header:    parentHeader,
				Events:    parentSession.Events(),
			},
		},
	)
	if seedErr != nil {
		return nil, seedErr
	}
	return cloneEvents(seedValue.Events), nil
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

func cloneEvents(source []session.Event) []session.Event {
	if source == nil {
		return nil
	}
	detached := make([]session.Event, len(source))
	for index, eventValue := range source {
		detached[index] = eventValue
		detached[index].Data = append([]byte(nil), eventValue.Data...)
		if eventValue.SourceEventSeqs != nil {
			sequences := append([]int64(nil), (*eventValue.SourceEventSeqs)...)
			detached[index].SourceEventSeqs = &sequences
		}
		if eventValue.SurfaceOp != nil {
			operation := *eventValue.SurfaceOp
			detached[index].SurfaceOp = &operation
		}
	}
	return detached
}
