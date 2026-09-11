// Package continuable owns durable Subagent creation and resumable executions.
package continuable

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/agentclient"
	"github.com/gorenx/goren/subagent/internal/childpolicy"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
	"github.com/gorenx/goren/subagent/internal/lineage"
	"github.com/gorenx/goren/subagent/internal/seedbuilder"
)

type SeedBuilders interface {
	Find(string) (subagent.SeedBuilder, bool)
}
type Agents interface {
	Find(context.Context, session.SessionID) (agentclient.Agent, error)
	CreateChild(context.Context, agentregistry.CreateChildAgent) (agentclient.Agent, error)
}
type History interface {
	Load(context.Context, session.SessionID) (*persistence.Log, error)
}

type FinalFlushFailure struct {
	ChildID session.SessionID
	Error   error
}
type FailureReporter interface{ ReportFinalFlushFailure(FinalFlushFailure) }

type Dependencies struct {
	Agents       Agents
	History      History
	SeedBuilders SeedBuilders
	Publisher    sharedexecution.EventPublisher
	Extensions   subagent.Extension
	Failures     FailureReporter
	Executions   *sharedexecution.Registry
}

type Service struct {
	dependencies Dependencies
	mutex        sync.Mutex
	residents    map[session.SessionID]*residentExecution
	starting     map[session.SessionID]bool
	closing      bool
}

func (*Service) Mode() subagent.Mode { return subagent.ModeContinuable }

func New(dependencies Dependencies) (*Service, error) {
	if dependencies.Agents == nil || dependencies.History == nil || dependencies.SeedBuilders == nil || dependencies.Failures == nil || dependencies.Executions == nil {
		return nil, errors.New("subagent: Continuable requires Agent directory, history, SeedBuilders, failure reporter, and Execution Registry")
	}
	return &Service{
		dependencies: dependencies,
		residents:    make(map[session.SessionID]*residentExecution),
		starting:     make(map[session.SessionID]bool),
	}, nil
}

func (owner *Service) Start(ctx context.Context, command subagent.ContinuableStartCommand) (subagent.Execution, error) {
	if err := checkContext(ctx, "Continuable Start"); err != nil {
		return nil, err
	}
	request, err := command.Snapshot()
	if err != nil {
		return nil, err
	}
	parent, err := owner.dependencies.Agents.Find(ctx, request.ParentID)
	if err != nil {
		return nil, unauthorized("Continuable operation requires the exact live parent Agent")
	}
	parentView, err := parent.Read(ctx)
	if err != nil {
		return nil, err
	}
	childID, err := requestedChildID(command.RequestedChildID())
	if err != nil {
		return nil, err
	}
	if err = owner.claim(childID); err != nil {
		return nil, err
	}
	defer owner.releaseClaim(childID)
	if command.RequestedChildID() != nil {
		if stored, loadErr := owner.dependencies.History.Load(ctx, childID); loadErr == nil && stored != nil {
			return nil, duplicateChild(childID)
		} else if loadErr != nil && !isNotFound(loadErr) {
			return nil, loadErr
		}
	}
	if request.AgentOptions == nil {
		return nil, errors.New("subagent: Continuable descriptor requires Agent options")
	}
	descriptor, err := continuableDescriptor(command.SeedBuilderName(), command.Label(), request)
	if err != nil {
		return nil, err
	}
	builder, found := owner.dependencies.SeedBuilders.Find(command.SeedBuilderName())
	if !found {
		return nil, noSeedBuilder(command.SeedBuilderName())
	}
	seedValue, err := builder.BuildSeed(ctx, parentView.Snapshot.Events)
	if err != nil {
		return nil, err
	}
	builderSeed := seedValue.EventPrefix()
	seed, err := childpolicy.AppendDelegation(builderSeed)
	if err != nil {
		return nil, err
	}
	seed, err = seedbuilder.AppendDescriptor(childID, seed, descriptor)
	if err != nil {
		return nil, err
	}
	childLineage, err := lineage.From(request.ParentID, parentView.Header, agent.Options{}, request.MaxDepth)
	if err != nil {
		return nil, err
	}
	configuration, err := owner.configure(ctx, descriptor)
	if err != nil {
		return nil, err
	}
	creationID, err := sharedexecution.NewRunID()
	if err != nil {
		return nil, err
	}
	child, err := owner.dependencies.Agents.CreateChild(ctx, agentregistry.CreateChildAgent{
		Parent: parent.Creation(),
		Child: agentregistry.Creation{
			AgentID:    childID,
			CreationID: string(creationID),
		},
		Options:        session.CreateOptions{Seed: seed, Metadata: childLineage.Metadata(int64(len(builderSeed)))},
		AgentOptions:   request.AgentOptions,
		Tools:          &configuration.Tools,
		PromptSections: configuration.PromptSections,
	})
	if err != nil {
		return nil, err
	}
	prompt, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{Content: request.Prompt, Source: agentmessage.UserMessageSource{}})
	if err != nil {
		return nil, errors.Join(err, child.Close(context.WithoutCancel(ctx)))
	}
	if err = child.Followup(ctx, prompt); err != nil {
		return nil, errors.Join(err, child.Close(context.WithoutCancel(ctx)))
	}
	return owner.publish(child, parent, command.SeedBuilderName(), int64(len(seed)))
}

func (owner *Service) Resume(ctx context.Context, parentID session.SessionID, childID session.SessionID, message agentmessage.UserMessage) (agentmessage.MessageID, error) {
	if err := checkContext(ctx, "Continuable Resume"); err != nil {
		return "", err
	}
	parent, err := owner.dependencies.Agents.Find(ctx, parentID)
	if err != nil {
		return "", unauthorized("Continuable operation requires the exact live parent Agent")
	}
	owner.mutex.Lock()
	resident := owner.residents[childID]
	owner.mutex.Unlock()
	if resident != nil && resident.State() == subagent.ExecutionActive {
		if resident.parent != parent.Creation() {
			return "", unauthorizedChild(childID)
		}
		if err = resident.child.Followup(ctx, message); err != nil {
			return "", err
		}
		return message.StableID(), nil
	}
	if err = owner.claim(childID); err != nil {
		return "", err
	}
	defer owner.releaseClaim(childID)
	stored, err := owner.dependencies.History.Load(ctx, childID)
	if err != nil || stored == nil {
		return "", notResumable(childID, "is unavailable", err)
	}
	if stored.Header.ParentSession == nil || *stored.Header.ParentSession != parentID {
		return "", unauthorizedChild(childID)
	}
	descriptorValue, found, err := subagent.FoldDescriptor(stored.Events)
	if err != nil || !found {
		return "", notResumable(childID, "has no Continuable descriptor", err)
	}
	descriptor, ok := descriptorValue.(subagent.ContinuableDescriptor)
	if !ok {
		return "", notResumable(childID, "is not Continuable", nil)
	}
	configuration, err := owner.configure(ctx, descriptor)
	if err != nil {
		return "", err
	}
	creationID, err := sharedexecution.NewRunID()
	if err != nil {
		return "", err
	}
	child, err := owner.dependencies.Agents.CreateChild(ctx, agentregistry.CreateChildAgent{
		Parent: parent.Creation(),
		Child: agentregistry.Creation{
			AgentID:    childID,
			CreationID: string(creationID),
		},
		Restore:        true,
		AgentOptions:   descriptorOptions(descriptor),
		Tools:          &configuration.Tools,
		PromptSections: configuration.PromptSections,
	})
	if err != nil {
		return "", notResumable(childID, "is unavailable", err)
	}
	view, err := child.Read(ctx)
	if err != nil {
		return "", errors.Join(err, child.Close(context.WithoutCancel(ctx)))
	}
	if err = child.Followup(ctx, message); err != nil {
		return "", errors.Join(err, child.Close(context.WithoutCancel(ctx)))
	}
	if _, err = owner.publish(child, parent, descriptor.Provider, int64(len(view.Snapshot.Events))); err != nil {
		return "", err
	}
	return message.StableID(), nil
}

func (owner *Service) Interrupt(ctx context.Context, childID session.SessionID) error {
	if err := checkContext(ctx, "Continuable Interrupt"); err != nil {
		return err
	}
	owner.mutex.Lock()
	resident := owner.residents[childID]
	owner.mutex.Unlock()
	if resident == nil {
		return nil
	}
	return resident.child.Cancel(ctx, agent.ParentCancel{}, agent.CancelOptions{KeepInbox: true})
}

func (owner *Service) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	owner.mutex.Lock()
	owner.closing = true
	var targets []*residentExecution
	for _, resident := range owner.residents {
		targets = append(targets, resident)
	}
	owner.mutex.Unlock()
	var closeErr error
	for _, resident := range targets {
		closeErr = errors.Join(closeErr, resident.StopAndWait(ctx, sharedexecution.CloseModule))
	}
	return closeErr
}

func (owner *Service) configure(ctx context.Context, descriptor subagent.ContinuableDescriptor) (subagent.ChildConfiguration, error) {
	configuration := subagent.ChildConfiguration{}
	childpolicy.Apply(childpolicy.PolicySet{Persona: descriptor.Persona, ToolRestriction: descriptor.ToolFilter}, &configuration)
	if owner.dependencies.Extensions != nil {
		if err := owner.dependencies.Extensions.ConfigureChild(ctx, &configuration); err != nil {
			return configuration, err
		}
	}
	return configuration, nil
}

func (owner *Service) publish(child agentclient.Agent, parent agentclient.Agent, provider string, boundary int64) (subagent.Execution, error) {
	runID, err := sharedexecution.NewRunID()
	if err != nil {
		return nil, err
	}
	resident := newResidentExecution(owner, runID, child, parent.Creation(), provider, boundary)
	owner.mutex.Lock()
	owner.residents[child.ID()] = resident
	owner.mutex.Unlock()
	if err = sharedexecution.Publish(owner.dependencies.Executions, owner.dependencies.Publisher, sharedexecution.Entry{Execution: resident, Mode: subagent.ModeContinuable, Parent: parent.Creation(), Subject: child.Creation()}, provider); err != nil {
		owner.remove(resident)
		return nil, errors.Join(err, child.Close(context.Background()))
	}
	go resident.watch()
	return resident, nil
}

func (owner *Service) remove(resident *residentExecution) {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.residents[resident.ChildID()] == resident {
		delete(owner.residents, resident.ChildID())
	}
}
func (owner *Service) claim(id session.SessionID) error {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.closing {
		return errors.New("subagent: Continuable children are closing")
	}
	if owner.starting[id] || owner.residents[id] != nil {
		return duplicateChild(id)
	}
	owner.starting[id] = true
	return nil
}
func (owner *Service) releaseClaim(id session.SessionID) {
	owner.mutex.Lock()
	delete(owner.starting, id)
	owner.mutex.Unlock()
}
func checkContext(ctx context.Context, operation string) error {
	if ctx == nil {
		return errors.New("subagent: " + operation + " context is nil")
	}
	return context.Cause(ctx)
}
func requestedChildID(requested *session.SessionID) (session.SessionID, error) {
	if requested == nil {
		return sharedexecution.NewChildID()
	}
	if *requested == "" {
		return "", errors.New("subagent: Continuable child ID is empty")
	}
	return *requested, nil
}
func isNotFound(err error) bool {
	var missing *persistence.NotFoundError
	return errors.As(err, &missing)
}
func descriptorOptions(descriptor subagent.ContinuableDescriptor) *agent.Options {
	return &agent.Options{Provider: stringValue(descriptor.AgentProvider), Model: stringValue(descriptor.AgentModel)}
}
func continuableDescriptor(provider, label string, request subagent.ContinuableOptions) (subagent.ContinuableDescriptor, error) {
	descriptor := subagent.ContinuableDescriptor{Provider: provider, Label: label, AgentProvider: stringPointer(request.AgentOptions.Provider), AgentModel: stringPointer(request.AgentOptions.Model), Persona: request.Persona, ToolFilter: request.ToolFilter}
	snapshot, err := subagent.SnapshotDescriptor(descriptor)
	if err != nil {
		return subagent.ContinuableDescriptor{}, err
	}
	result, ok := snapshot.DescriptorValue().(subagent.ContinuableDescriptor)
	if !ok {
		return result, errors.New("subagent: Continuable descriptor snapshot changed variant")
	}
	return result, nil
}
func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
