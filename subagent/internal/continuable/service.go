// Package continuable owns resumable Subagent Session identity, exact
// execution materialization, messaging, and settlement policy.
package continuable

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
	"github.com/gorenx/goren/subagent/internal/lineage"
	"github.com/gorenx/goren/subagent/internal/seedbuilder"
)

// SeedBuilders resolves the exact registered child seed strategy.
type SeedBuilders interface {
	Find(string) (subagent.SeedBuilder, bool)
}

// FinalFlushFailure identifies a contained durability failure.
type FinalFlushFailure struct {
	ChildID session.SessionID
	Error   error
}

// FailureReporter receives failures that cannot change lifecycle completion.
type FailureReporter interface {
	ReportFinalFlushFailure(FinalFlushFailure)
}

// Dependencies contains the capabilities required by Continuable execution.
type Dependencies struct {
	Agents       agent.Registry
	Constructor  agent.Constructor
	Descendants  agent.RuntimeDescendants
	Sessions     session.LiveStore
	Persistence  persistence.Persistence
	SeedBuilders SeedBuilders
	Publisher    sharedexecution.EventPublisher
	Delegation   approval.DelegationPolicy
	Extensions   agent.Provisioner
	Failures     FailureReporter
	Executions   *sharedexecution.Registry
}

// Service orchestrates Continuable use cases through the materialization
// policy and child owners. Module admission belongs to
// subagents.Service; Execution phase belongs to the shared execution object.
type Service struct {
	dependencies Dependencies
	materializer *materializer
	children     *continuableChildRegistry
}

// Mode identifies the business mode implemented by Service.
func (*Service) Mode() subagent.Mode {
	return subagent.ModeContinuable
}

// New constructs an accepting Continuable Service.
func New(dependencySet Dependencies) (*Service, error) {
	if dependencySet.Agents == nil || dependencySet.Constructor == nil ||
		dependencySet.Descendants == nil || dependencySet.Sessions == nil ||
		dependencySet.Persistence == nil || dependencySet.SeedBuilders == nil ||
		dependencySet.Failures == nil ||
		dependencySet.Executions == nil {
		return nil, errors.New(
			"subagent: Continuable requires Agent Registry, Constructor, " +
				"runtime descendants, Session LiveStore, persistence, " +
				"SeedBuilders, failure reporter, and " +
				"Execution Registry",
		)
	}
	owner := &Service{
		dependencies: dependencySet,
		materializer: &materializer{
			agents:      dependencySet.Agents,
			constructor: dependencySet.Constructor,
			sessions:    dependencySet.Sessions,
			persistence: dependencySet.Persistence,
			delegation:  dependencySet.Delegation,
			extensions:  dependencySet.Extensions,
		},
	}
	owner.children = newContinuableChildRegistry(
		dependencySet,
		owner.materializer,
	)
	return owner, nil
}

// Close rejects new work, requests every current Execution to stop, and waits
// only until each exact Agent enters Closing. Agent owns Scope teardown.
func (owner *Service) Close(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	return owner.children.close(closeContext)
}

func requireLiveParent(
	agents agent.Registry,
	parentAgent agent.Agent,
) error {
	if parentAgent == nil || !agents.Contains(parentAgent) {
		return unauthorized(
			"Continuable operation requires the exact live parent Agent",
		)
	}
	return nil
}

func checkContext(ctx context.Context, operation string) error {
	if ctx == nil {
		return errors.New("subagent: " + operation + " context is nil")
	}
	return ctx.Err()
}

// Start creates one durable child and returns its first common Execution.
func (owner *Service) Start(
	ctx context.Context,
	command subagent.ContinuableStartCommand,
) (subagent.Execution, error) {
	if contextErr := checkContext(ctx, "Continuable Start"); contextErr != nil {
		return nil, contextErr
	}
	requestSnapshot, snapshotErr := command.Snapshot()
	if snapshotErr != nil {
		return nil, snapshotErr
	}
	if authorizationErr := requireLiveParent(
		owner.dependencies.Agents,
		requestSnapshot.Parent,
	); authorizationErr != nil {
		return nil, authorizationErr
	}
	seedBuilderName := command.SeedBuilderName()
	childLineage, lineageErr := lineage.From(
		requestSnapshot.Parent,
		requestSnapshot.MaxDepth,
	)
	if lineageErr != nil {
		return nil, lineageErr
	}
	resolvedOptions := childLineage.AgentOptions(requestSnapshot.AgentOptions)
	requestSnapshot.AgentOptions = &resolvedOptions
	descriptor, descriptorErr := continuableDescriptor(
		seedBuilderName,
		command.Label(),
		requestSnapshot,
	)
	if descriptorErr != nil {
		return nil, descriptorErr
	}
	requestedID := command.RequestedChildID()
	childID, childIDErr := requestedChildID(requestedID)
	if childIDErr != nil {
		return nil, childIDErr
	}
	builder, found := owner.dependencies.SeedBuilders.Find(
		seedBuilderName,
	)
	if !found {
		return nil, noSeedBuilder(seedBuilderName)
	}
	parentSession := requestSnapshot.Parent.SessionValue()
	if parentSession == nil {
		return nil, errors.New("subagent: parent Session is unavailable")
	}
	seedValue, seedErr := builder.BuildSeed(
		ctx,
		parentSession.Events(),
	)
	if seedErr != nil {
		return nil, seedErr
	}
	builderSeed := seedValue.EventPrefix()
	seed, seedErr := seedbuilder.AppendDescriptor(
		childID,
		builderSeed,
		descriptor,
	)
	if seedErr != nil {
		return nil, seedErr
	}
	input := startInput{
		parent:            requestSnapshot.Parent,
		descriptor:        descriptor,
		request:           requestSnapshot,
		lineage:           childLineage,
		seed:              seed,
		seedBuilder:       seedBuilderName,
		seedLength:        int64(len(builderSeed)),
		identityRequested: requestedID != nil,
	}
	for {
		child, acquireErr := owner.children.acquire(childID)
		if acquireErr != nil {
			return nil, acquireErr
		}
		running, startErr := child.start(ctx, input)
		if errors.Is(startErr, errChildRetired) {
			continue
		}
		return running, startErr
	}
}

// Resume materializes a durable child and atomically accepts the first message
// of the new Agent epoch. If another caller made the child resident first, the
// same slot serializes delivery to that exact Agent.
func (owner *Service) Resume(
	ctx context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	messageValue agentmessage.UserMessage,
) (agentmessage.MessageID, error) {
	if contextErr := checkContext(ctx, "Continuable Resume"); contextErr != nil {
		return "", contextErr
	}
	for {
		child, acquireErr := owner.children.acquire(childID)
		if acquireErr != nil {
			return "", acquireErr
		}
		identifier, resumeErr := child.resume(
			ctx,
			parentAgent,
			messageValue,
		)
		if errors.Is(resumeErr, errChildRetired) {
			continue
		}
		return identifier, resumeErr
	}
}

// Interrupt cancels the current turn while retaining pending Inbox messages
// and the durable child Session.
func (owner *Service) Interrupt(
	ctx context.Context,
	targetID session.SessionID,
) error {
	if contextErr := checkContext(
		ctx,
		"Continuable Interrupt",
	); contextErr != nil {
		return contextErr
	}
	return owner.children.interrupt(ctx, targetID)
}
