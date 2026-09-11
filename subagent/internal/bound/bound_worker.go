package bound

import (
	"bytes"
	"context"
	"errors"
	"sync"

	"github.com/bytedance/sonic"
	"github.com/gorenx/goren/agent"
	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/subagent"
	boundcontract "github.com/gorenx/goren/subagent/bound"
	"github.com/gorenx/goren/subagent/internal/agentclient"
	"github.com/gorenx/goren/subagent/internal/childpolicy"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
	"github.com/gorenx/goren/subagent/internal/lineage"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
	"github.com/gorenx/goren/subagent/internal/seedbuilder"
)

type boundWorker struct {
	mutex     sync.Mutex
	owner     *Service
	parentID  session.SessionID
	binding   subagentprojection.BoundBinding
	child     agentclient.Agent
	execution *boundExecution
	revision  int64
}

func (worker *boundWorker) materialize(ctx context.Context, parent agentclient.Agent, parentView sessionactorView, definition boundcontract.Definition) error {
	worker.mutex.Lock()
	defer worker.mutex.Unlock()
	if worker.child != nil && worker.revision == definition.Revision {
		return nil
	}
	if worker.execution != nil {
		if err := worker.execution.StopAndWait(ctx, sharedexecution.CloseDisposed); err != nil {
			return err
		}
		worker.child = nil
		worker.execution = nil
	}
	stored, loadErr := worker.owner.dependencies.History.Load(ctx, worker.binding.ChildSessionID)
	restore := loadErr == nil && stored != nil
	if loadErr != nil {
		var missing *persistence.NotFoundError
		if !errors.As(loadErr, &missing) {
			return worker.fail(ctx, parent, definition, loadErr)
		}
	}
	configuration := subagent.ChildConfiguration{}
	childpolicy.Apply(childpolicy.PolicySet{SystemPrompt: &definition.SystemPrompt, ToolRestriction: definition.ToolRestriction}, &configuration)
	if err := worker.owner.dependencies.Extensions.ConfigureCommon(ctx, &configuration); err != nil {
		return worker.fail(ctx, parent, definition, err)
	}
	if err := worker.owner.dependencies.Extensions.ConfigureSelected(ctx, definition.Extensions, &configuration); err != nil {
		return worker.fail(ctx, parent, definition, err)
	}
	options := session.CreateOptions{}
	if restore {
		if stored.Header.ParentSession == nil || *stored.Header.ParentSession != worker.parentID || stored.Header.Origin != session.OriginSubagent {
			return unauthorizedChild(worker.binding.ChildSessionID)
		}
		if worker.binding.ContextNextSeq < 0 || worker.binding.ContextNextSeq > int64(len(stored.Events)) {
			return worker.fail(
				ctx,
				parent,
				definition,
				errors.New("subagent: Bound Binding context prefix exceeds the stored child Session"),
			)
		}
		descriptor, found, err := subagent.FoldDescriptor(stored.Events[worker.binding.ContextNextSeq:])
		if err != nil || !found {
			return worker.fail(ctx, parent, definition, notResumable(worker.binding.ChildSessionID, "has no Bound descriptor", err))
		}
		boundDescriptor, ok := descriptor.(subagent.BoundDescriptor)
		if !ok || boundDescriptor.Name != worker.binding.Name {
			return worker.fail(ctx, parent, definition, notResumable(worker.binding.ChildSessionID, "does not match its Bound Binding", nil))
		}
	} else {
		if worker.binding.ContextNextSeq < 0 || worker.binding.ContextNextSeq > int64(len(parentView.Snapshot.Events)) {
			return errors.New("subagent: Bound Binding context prefix is invalid")
		}
		seed := subagent.NewSessionSeed(parentView.Snapshot.Events[:worker.binding.ContextNextSeq]).EventPrefix()
		var err error
		seed, err = childpolicy.AppendDelegation(seed)
		if err != nil {
			return worker.fail(ctx, parent, definition, err)
		}
		seed, err = seedbuilder.AppendDescriptor(worker.binding.ChildSessionID, seed, subagent.BoundDescriptor{Name: worker.binding.Name})
		if err != nil {
			return worker.fail(ctx, parent, definition, err)
		}
		childLineage, err := lineage.From(worker.parentID, parentView.Header, agent.Options{}, definition.MaxDepth)
		if err != nil {
			return worker.fail(ctx, parent, definition, err)
		}
		options = session.CreateOptions{Seed: seed, Metadata: childLineage.Metadata(worker.binding.ContextNextSeq)}
	}
	creationID, err := sharedexecution.NewRunID()
	if err != nil {
		return worker.fail(ctx, parent, definition, err)
	}
	child, err := worker.owner.dependencies.Agents.CreateChild(ctx, agentregistry.CreateChildAgent{Parent: parent.Creation(), Child: agentregistry.Creation{AgentID: worker.binding.ChildSessionID, CreationID: string(creationID)}, Restore: restore, Options: options, AgentOptions: definition.AgentOptions, Tools: &configuration.Tools, PromptSections: configuration.PromptSections})
	if err != nil {
		return worker.fail(ctx, parent, definition, err)
	}
	applied, err := session.NewEventDraft(boundcontract.DefinitionAppliedEvent, boundcontract.DefinitionAppliedData{Version: boundcontract.EventVersion, Definition: definition})
	if err == nil {
		var result session.WriteResult
		result, err = child.Commit(ctx, session.Batch(applied))
		if err == nil {
			err = child.Flush(ctx, result.Barrier)
		}
	}
	if err != nil {
		_ = child.Close(context.WithoutCancel(ctx))
		return worker.fail(ctx, parent, definition, err)
	}
	runID, err := sharedexecution.NewRunID()
	if err != nil {
		_ = child.Close(context.WithoutCancel(ctx))
		return worker.fail(ctx, parent, definition, err)
	}
	execution := newBoundExecution(worker.owner, runID, child, parent.Creation(), definition)
	if err = sharedexecution.Publish(worker.owner.dependencies.Executions, worker.owner.dependencies.Publisher, sharedexecution.Entry{Execution: execution, Mode: subagent.ModeBound, Parent: parent.Creation(), Subject: child.Creation()}, definition.Name); err != nil {
		_ = child.Close(context.WithoutCancel(ctx))
		return worker.fail(ctx, parent, definition, err)
	}
	worker.child, worker.execution, worker.revision = child, execution, definition.Revision
	return worker.record(ctx, parent, definition.Revision, boundcontract.MaterializationSucceeded)
}
func (worker *boundWorker) fail(ctx context.Context, parent agentclient.Agent, definition boundcontract.Definition, failure error) error {
	if worker.owner.dependencies.Failures != nil {
		worker.owner.dependencies.Failures.ReportBoundMaterializationFailure(MaterializationFailure{ParentID: worker.parentID, ChildID: worker.binding.ChildSessionID, Error: failure})
	}
	return errors.Join(failure, worker.record(context.WithoutCancel(ctx), parent, definition.Revision, boundcontract.MaterializationFailed))
}
func (worker *boundWorker) record(ctx context.Context, parent agentclient.Agent, revision int64, result boundcontract.MaterializationResult) error {
	draft, err := session.NewEventDraft(boundcontract.MaterializationEvent, boundcontract.MaterializationData{Version: boundcontract.EventVersion, Name: worker.binding.Name, ChildSessionID: worker.binding.ChildSessionID, DefinitionRevision: revision, Result: result})
	if err != nil {
		return err
	}
	committed, err := parent.Commit(ctx, session.Batch(draft))
	if err != nil {
		return err
	}
	return parent.Flush(ctx, committed.Barrier)
}
func (worker *boundWorker) followup(ctx context.Context, message agentmessage.UserMessage) (agentmessage.MessageID, error) {
	worker.mutex.Lock()
	child := worker.child
	worker.mutex.Unlock()
	if child == nil {
		return "", errors.New("subagent: Bound child is unavailable")
	}
	if err := child.Followup(ctx, message); err != nil {
		return "", err
	}
	view, err := child.Read(ctx)
	if err != nil {
		return "", err
	}
	if err = child.Flush(ctx, view.Snapshot.Barrier); err != nil {
		return "", err
	}
	return message.StableID(), nil
}
func (worker *boundWorker) deliver(ctx context.Context, input boundcontract.Input) (boundcontract.Receipt, error) {
	worker.mutex.Lock()
	child := worker.child
	worker.mutex.Unlock()
	if child == nil {
		return boundcontract.Receipt{}, errors.New("subagent: Bound child is unavailable")
	}
	view, err := child.Read(ctx)
	if err != nil {
		return boundcontract.Receipt{}, err
	}
	receipt, err := findInputReceipt(view.Header, view.Snapshot.Events, input)
	if err != nil || receipt.MessageID != "" {
		return receipt, err
	}
	source, err := boundcontract.NewDelivery(input)
	if err != nil {
		return receipt, err
	}
	message, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{Content: input.Content, Source: source})
	if err != nil {
		return receipt, err
	}
	if _, err = worker.followup(ctx, message); err != nil {
		return receipt, err
	}
	return boundcontract.Receipt{InputID: input.ID, MessageID: message.StableID()}, nil
}
func (worker *boundWorker) interrupt(ctx context.Context) error {
	worker.mutex.Lock()
	child := worker.child
	worker.mutex.Unlock()
	if child == nil {
		return nil
	}
	return child.Cancel(ctx, agent.ParentCancel{}, agent.CancelOptions{KeepInbox: true})
}
func (worker *boundWorker) close(ctx context.Context) error {
	worker.mutex.Lock()
	execution := worker.execution
	worker.mutex.Unlock()
	if execution == nil {
		return nil
	}
	return execution.StopAndWait(ctx, sharedexecution.CloseModule)
}

func findInputReceipt(header session.Header, events []session.Event, want boundcontract.Input) (boundcontract.Receipt, error) {
	start := int64(0)
	if header.SeedLength != nil {
		start = *header.SeedLength
	}
	var receipt boundcontract.Receipt
	found := false
	for _, event := range events {
		if event.Seq < start || event.Type != agent.InboxSplicedEventName {
			continue
		}
		var splice agent.InboxSplice
		if err := sonic.Unmarshal(event.Data, &splice); err != nil {
			return receipt, err
		}
		for _, message := range splice.Inserted {
			origin := message.SourceValue()
			if origin == nil || origin.SourceKind() != boundcontract.DeliveryKind {
				continue
			}
			delivery, err := boundcontract.DecodeDelivery(origin)
			if err != nil {
				return receipt, err
			}
			if delivery.Input != want.ID {
				continue
			}
			if found {
				return receipt, errors.New("subagent: child Session contains duplicate Bound delivery receipts")
			}
			expected, err := boundcontract.NewDelivery(want)
			if err != nil {
				return receipt, err
			}
			if !bytes.Equal(delivery.Origin, expected.Origin) {
				return receipt, errors.New("subagent: Bound Input ID was reused with different provenance")
			}
			received, _ := sonic.Marshal(message.ContentValue())
			wanted, _ := sonic.Marshal(want.Content)
			if !bytes.Equal(received, wanted) {
				return receipt, errors.New("subagent: Bound Input ID was reused with different content")
			}
			receipt = boundcontract.Receipt{InputID: want.ID, MessageID: message.StableID()}
			found = true
		}
	}
	return receipt, nil
}
