// Package continuable owns resumable Subagent Session identity, exact
// execution materialization, messaging, and settlement policy.
package continuable

import (
	"context"
	"errors"
	"fmt"

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
// policy and resident execution owner. Module admission belongs to
// subagents.Service; Execution phase belongs to the shared execution object.
type Service struct {
	dependencies Dependencies
	materializer materializer
	residents    *residentExecutions
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
	return &Service{
		dependencies: dependencySet,
		materializer: materializer{
			agents:      dependencySet.Agents,
			constructor: dependencySet.Constructor,
			sessions:    dependencySet.Sessions,
			persistence: dependencySet.Persistence,
			delegation:  dependencySet.Delegation,
			extensions:  dependencySet.Extensions,
		},
		residents: newResidentExecutions(dependencySet),
	}, nil
}

// Close rejects new work, requests every current Execution to stop, and waits
// only until each exact Agent enters Closing. Agent owns Scope teardown.
func (owner *Service) Close(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	return owner.residents.close(closeContext)
}

func (owner *Service) authorizeParent(parentAgent agent.Agent) error {
	if parentAgent == nil || !owner.dependencies.Agents.Contains(parentAgent) {
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
	if authorizationErr := owner.authorizeParent(requestSnapshot.Parent); authorizationErr != nil {
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
	slot := owner.residents.acquire(childID)
	defer owner.residents.release(childID, slot)
	slot.mutex.Lock()
	defer slot.mutex.Unlock()
	if requestErr := ctx.Err(); requestErr != nil {
		return nil, requestErr
	}
	if authorizationErr := owner.authorizeParent(requestSnapshot.Parent); authorizationErr != nil {
		return nil, authorizationErr
	}
	if slot.current != nil {
		return nil, duplicateChild(childID)
	}
	if availabilityErr := owner.materializer.assertAvailable(
		childID,
	); availabilityErr != nil {
		return nil, availabilityErr
	}
	if requestedID != nil {
		if availabilityErr := owner.materializer.assertPersistedAvailable(
			ctx,
			childID,
		); availabilityErr != nil {
			return nil, availabilityErr
		}
	}
	handle, createErr := owner.materializer.create(
		ctx,
		childID,
		descriptor,
		requestSnapshot,
		childLineage,
		seed,
		int64(len(builderSeed)),
	)
	if createErr != nil {
		return nil, createErr
	}
	prompt, messageErr := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: requestSnapshot.Prompt,
		Source:  agentmessage.UserMessageSource{},
	})
	if messageErr != nil {
		return nil, errors.Join(
			messageErr,
			handle.Dispose(context.WithoutCancel(ctx)),
		)
	}
	if submitErr := handle.Subject.Followup(prompt); submitErr != nil {
		return nil, errors.Join(
			submitErr,
			handle.Dispose(context.WithoutCancel(ctx)),
		)
	}
	current, publishErr := owner.residents.publish(
		handle,
		requestSnapshot.Parent,
		seedBuilderName,
		slot,
	)
	if publishErr != nil {
		return nil, errors.Join(
			publishErr,
			handle.Dispose(context.WithoutCancel(ctx)),
		)
	}
	owner.residents.watch(current)
	return current.running, nil
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
		slot := owner.residents.acquire(childID)
		slot.mutex.Lock()
		if authorizationErr := owner.authorizeParent(parentAgent); authorizationErr != nil {
			slot.mutex.Unlock()
			owner.residents.release(childID, slot)
			return "", authorizationErr
		}
		current := slot.current
		if current != nil && current.running.State() != subagent.ExecutionActive {
			running := current.running
			slot.mutex.Unlock()
			owner.residents.release(childID, slot)
			if waitErr := running.Wait(ctx); waitErr != nil {
				return "", waitErr
			}
			continue
		}
		if current == nil {
			handle, seedBuilder, resumeErr := owner.materializer.resume(
				ctx,
				parentAgent,
				childID,
			)
			if resumeErr != nil {
				slot.mutex.Unlock()
				owner.residents.release(childID, slot)
				return "", resumeErr
			}
			if submitErr := handle.Subject.Followup(messageValue); submitErr != nil {
				slot.mutex.Unlock()
				owner.residents.release(childID, slot)
				return "", errors.Join(
					submitErr,
					handle.Dispose(context.WithoutCancel(ctx)),
				)
			}
			current, resumeErr = owner.residents.publish(
				handle,
				parentAgent,
				seedBuilder,
				slot,
			)
			if resumeErr != nil {
				slot.mutex.Unlock()
				owner.residents.release(childID, slot)
				return "", errors.Join(
					resumeErr,
					handle.Dispose(context.WithoutCancel(ctx)),
				)
			}
			owner.residents.watch(current)
		} else {
			if current.terminator.parent.ID() != parentAgent.ID() ||
				!owner.dependencies.Agents.Contains(parentAgent) {
				slot.mutex.Unlock()
				owner.residents.release(childID, slot)
				return "", unauthorized(
					fmt.Sprintf(
						"subagent %q delivery requires its exact live parent",
						childID,
					),
				)
			}
			if submitErr := current.terminator.handle.Subject.Followup(
				messageValue,
			); submitErr != nil {
				slot.mutex.Unlock()
				owner.residents.release(childID, slot)
				return "", submitErr
			}
			signal(current)
		}
		slot.mutex.Unlock()
		owner.residents.release(childID, slot)
		return messageValue.StableID(), nil
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
	owner.residents.interrupt(targetID)
	return nil
}
