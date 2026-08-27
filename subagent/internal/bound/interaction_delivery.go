package bound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
)

type interactionWorker struct {
	owner     *Service
	key       operationKey
	parent    agent.Agent
	operation *operation
	floor     int64
	dirty     chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	stopOnce  sync.Once
}

func newInteractionWorker(
	owner *Service,
	parentAgent agent.Agent,
	binding subagentprojection.BoundBinding,
	childSession session.Context,
	currentOperation *operation,
) *interactionWorker {
	workerContext, cancelWorker := context.WithCancel(context.Background())
	floor := binding.Seq + 1
	if seedLength := childSession.Header().SeedLength; seedLength != nil &&
		*seedLength > floor {
		floor = *seedLength
	}
	return &interactionWorker{
		owner: owner,
		key: operationKey{
			parentID: parentAgent.ID(),
			childID:  binding.ChildSessionID,
		},
		parent:    parentAgent,
		operation: currentOperation,
		floor:     floor,
		dirty:     make(chan struct{}, 1),
		ctx:       workerContext,
		cancel:    cancelWorker,
		done:      make(chan struct{}),
	}
}

func (owner *Service) ensureInteractionWorker(
	parentAgent agent.Agent,
	binding subagentprojection.BoundBinding,
	childSession session.Context,
	currentOperation *operation,
) {
	key := operationKey{
		parentID: parentAgent.ID(),
		childID:  binding.ChildSessionID,
	}
	owner.mutex.Lock()
	if owner.closing {
		owner.mutex.Unlock()
		return
	}
	parentWorkers := owner.workers[key.parentID]
	current := parentWorkers[key.childID]
	if current != nil && agent.Same(current.parent, parentAgent) {
		owner.mutex.Unlock()
		current.Notify()
		return
	}
	worker := newInteractionWorker(
		owner,
		parentAgent,
		binding,
		childSession,
		currentOperation,
	)
	if parentWorkers == nil {
		parentWorkers = make(map[session.SessionID]*interactionWorker)
		owner.workers[key.parentID] = parentWorkers
	}
	parentWorkers[key.childID] = worker
	owner.mutex.Unlock()
	if current != nil {
		current.stopOnce.Do(current.cancel)
	}
	go worker.Run()
	worker.Notify()
}

// SessionEventAppended coalesces only completed parent turns into existing
// per-binding workers. It performs no Session reads or cross-Agent effects.
func (owner *Service) SessionEventAppended(fact session.EventAppended) {
	if owner == nil || fact.Conversation == nil ||
		fact.Committed.Type != session.TurnEndEventName {
		return
	}
	owner.mutex.Lock()
	parentWorkers := owner.workers[fact.Conversation.ID()]
	workers := make([]*interactionWorker, 0, len(parentWorkers))
	for _, worker := range parentWorkers {
		workers = append(workers, worker)
	}
	owner.mutex.Unlock()
	for _, worker := range workers {
		worker.Notify()
	}
}

// AgentDisposed removes workers only for the exact parent Agent epoch.
func (owner *Service) AgentDisposed(
	_ context.Context,
	subject agent.Agent,
) error {
	if owner == nil || subject == nil {
		return nil
	}
	owner.mutex.Lock()
	parentWorkers := owner.workers[subject.ID()]
	workers := make([]*interactionWorker, 0, len(parentWorkers))
	for childID, worker := range parentWorkers {
		if !agent.Same(worker.parent, subject) {
			continue
		}
		workers = append(workers, worker)
		delete(parentWorkers, childID)
	}
	if len(parentWorkers) == 0 {
		delete(owner.workers, subject.ID())
	}
	owner.mutex.Unlock()
	for _, worker := range workers {
		worker.Stop()
	}
	return nil
}

func (worker *interactionWorker) Notify() {
	select {
	case worker.dirty <- struct{}{}:
	default:
	}
}

func (worker *interactionWorker) Run() {
	defer close(worker.done)
	for {
		select {
		case <-worker.ctx.Done():
			return
		case <-worker.dirty:
			if err := worker.catchUp(); err != nil {
				worker.owner.reportInteractionFailure(worker.key, err)
			}
		}
	}
}

func (worker *interactionWorker) Stop() {
	worker.stopOnce.Do(worker.cancel)
	<-worker.done
}

func (worker *interactionWorker) catchUp() error {
	for {
		advanced, err := worker.advanceOne()
		if err != nil || !advanced {
			return err
		}
	}
}

func (worker *interactionWorker) advanceOne() (bool, error) {
	if err := context.Cause(worker.ctx); err != nil {
		return false, nil
	}
	if worker.parent == nil || worker.parent.SessionValue() == nil ||
		worker.owner.dependencies.Agents == nil ||
		!worker.owner.dependencies.Agents.Contains(worker.parent) {
		return false, nil
	}
	parentSession := worker.parent.SessionValue()
	snapshot := parentSession.Snapshot()
	nextSeq, err := boundCursor(
		snapshot.Events,
		worker.key.childID,
		worker.floor,
	)
	if err != nil {
		return false, err
	}
	interaction, found, err := nextParentInteraction(snapshot.Events, nextSeq)
	if err != nil || !found {
		return false, err
	}
	if worker.owner.dependencies.Sessions == nil {
		return false, unavailableDependency("Session LiveStore")
	}
	if err = worker.owner.dependencies.Sessions.Flush(
		worker.ctx,
		parentSession,
	); err != nil {
		return false, err
	}
	worker.operation.mutex.Lock()
	defer worker.operation.mutex.Unlock()
	current, enabled, err := worker.currentExecution()
	if err != nil || !enabled {
		return false, err
	}
	disposition := subagent.BoundCursorSkipped
	if interaction.deliverable {
		delivery := subagent.Delivery{
			ParentSessionID: worker.key.parentID,
			Turn:            interaction.turn,
			FromSeq:         interaction.fromSeq,
			ThroughSeq:      interaction.nextSeq - 1,
			Outcome:         interaction.outcome,
		}
		count, receiptErr := countReceipts(
			current.terminator.handle.Subject.SessionValue(),
			delivery,
		)
		if receiptErr != nil {
			return false, receiptErr
		}
		if count == 0 {
			messageValue, messageErr := agentmessage.NewUserMessage(
				agentmessage.UserMessageInput{
					Content: interaction.content,
					Source:  delivery,
				},
			)
			if messageErr != nil {
				return false, messageErr
			}
			if messageErr = current.terminator.handle.Subject.Followup(
				messageValue,
			); messageErr != nil {
				return false, messageErr
			}
		}
		if err = worker.owner.dependencies.Sessions.Flush(
			worker.ctx,
			current.terminator.handle.Subject.SessionValue(),
		); err != nil {
			return false, err
		}
		disposition = subagent.BoundCursorDelivered
	}
	_, err = parentSession.Commit(
		worker.ctx,
		advanceCursorPlan{
			childID:     worker.key.childID,
			floor:       worker.floor,
			expected:    nextSeq,
			interaction: interaction,
			disposition: disposition,
		},
	)
	if err != nil {
		return false, err
	}
	if err = worker.owner.dependencies.Sessions.Flush(
		worker.ctx,
		parentSession,
	); err != nil {
		return false, err
	}
	return true, nil
}

func (worker *interactionWorker) currentExecution() (
	*currentExecution,
	bool,
	error,
) {
	view, err := worker.owner.parentView(worker.parent.SessionValue())
	if err != nil {
		return nil, false, err
	}
	config, found := view.Config(worker.key.childID)
	if !found {
		return nil, false, errors.New("subagent: Bound binding has no config")
	}
	if !config.Config.Enabled {
		return nil, false, nil
	}
	current := worker.operation.loadCurrent()
	if current == nil || current.running.State() != subagent.ExecutionActive ||
		current.revision != config.Revision ||
		!agent.Same(current.terminator.parent, worker.parent) {
		return nil, false, nil
	}
	return current, true, nil
}

func countReceipts(
	childSession session.Context,
	want subagent.Delivery,
) (int, error) {
	if childSession == nil {
		return 0, errors.New("subagent: Bound child Session is unavailable")
	}
	canonical, err := want.CloneSource()
	if err != nil {
		return 0, err
	}
	want = canonical.(subagent.Delivery)
	startSeq := int64(0)
	if seedLength := childSession.Header().SeedLength; seedLength != nil {
		startSeq = *seedLength
	}
	count := 0
	for _, committed := range childSession.Events() {
		if committed.Seq < startSeq || committed.Type != agent.InboxSplicedEventName {
			continue
		}
		var splice agent.InboxSplice
		if err := json.Unmarshal(committed.Data, &splice); err != nil {
			return 0, fmt.Errorf(
				"subagent: decode child Inbox receipt at seq %d: %w",
				committed.Seq,
				err,
			)
		}
		for _, messageValue := range splice.Inserted {
			origin := messageValue.SourceValue()
			if origin == nil || origin.SourceKind() != subagent.DeliveryKind {
				continue
			}
			delivery, err := subagent.DecodeDelivery(origin)
			if err != nil {
				return 0, fmt.Errorf(
					"subagent: decode child interaction receipt at seq %d: %w",
					committed.Seq,
					err,
				)
			}
			if delivery == want {
				count++
			}
		}
	}
	if count > 1 {
		return 0, errors.New(
			"subagent: child Session contains duplicate parent interaction receipts",
		)
	}
	return count, nil
}

type advanceCursorPlan struct {
	childID     session.SessionID
	floor       int64
	expected    int64
	interaction parentInteraction
	disposition subagent.BoundCursorDisposition
}

func (plan advanceCursorPlan) Build(
	_ context.Context,
	snapshot session.Snapshot,
) ([]session.EventDraft, error) {
	current, err := boundCursor(snapshot.Events, plan.childID, plan.floor)
	if err != nil {
		return nil, err
	}
	if current != plan.expected {
		return nil, fmt.Errorf(
			"subagent: Bound cursor moved to %d while expecting %d",
			current,
			plan.expected,
		)
	}
	draft, err := session.NewEventDraft(
		subagent.BoundCursorEvent,
		subagent.BoundCursor{
			Version:         subagent.BoundEventVersion,
			ChildSessionID:  plan.childID,
			PreviousNextSeq: plan.expected,
			NextSeq:         plan.interaction.nextSeq,
			ThroughTurn:     plan.interaction.turn,
			Disposition:     plan.disposition,
		},
	)
	if err != nil {
		return nil, err
	}
	return []session.EventDraft{draft}, nil
}

func (owner *Service) reportInteractionFailure(
	key operationKey,
	err error,
) {
	if owner.dependencies.Failures == nil || err == nil {
		return
	}
	owner.dependencies.Failures.ReportBoundInteractionFailure(
		InteractionFailure{
			ParentID: key.parentID,
			ChildID:  key.childID,
			Error:    err,
		},
	)
}

var _ session.WritePlan = advanceCursorPlan{}
