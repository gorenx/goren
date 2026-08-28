package bound

import (
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
)

// deliverySupervisor owns the delivery registry and its shutdown boundary.
// Each delivery belongs to one exact parent Agent epoch and child binding.
type deliverySupervisor struct {
	agents      agent.Registry
	sessions    session.LiveStore
	projections sessionprojection.Registry
	failures    FailureReporter
	mutex       sync.Mutex
	entries     map[session.SessionID]map[session.SessionID]*interactionDelivery
	closing     bool
}

func newDeliverySupervisor(dependencySet Dependencies) *deliverySupervisor {
	return &deliverySupervisor{
		agents:      dependencySet.Agents,
		sessions:    dependencySet.Sessions,
		projections: dependencySet.Projections,
		failures:    dependencySet.Failures,
		entries:     make(map[session.SessionID]map[session.SessionID]*interactionDelivery),
	}
}

func (supervisor *deliverySupervisor) ensure(
	parentAgent agent.Agent,
	binding subagentprojection.BoundBinding,
	childSession session.Context,
	slot *bindingSlot,
) {
	key := bindingKey{
		parentID: parentAgent.ID(),
		childID:  binding.ChildSessionID,
	}
	supervisor.mutex.Lock()
	if supervisor.closing {
		supervisor.mutex.Unlock()
		return
	}
	parentDeliveries := supervisor.entries[key.parentID]
	current := parentDeliveries[key.childID]
	if current != nil && agent.Same(current.parent, parentAgent) {
		supervisor.mutex.Unlock()
		current.Notify()
		return
	}
	delivery := newInteractionDelivery(
		supervisor,
		parentAgent,
		binding,
		childSession,
		slot,
	)
	if parentDeliveries == nil {
		parentDeliveries = make(map[session.SessionID]*interactionDelivery)
		supervisor.entries[key.parentID] = parentDeliveries
	}
	parentDeliveries[key.childID] = delivery
	supervisor.mutex.Unlock()
	if current != nil {
		current.stopOnce.Do(current.cancel)
	}
	go delivery.Run()
	delivery.Notify()
}

func (supervisor *deliverySupervisor) sessionEventAppended(
	fact session.EventAppended,
) {
	if supervisor == nil || fact.Conversation == nil ||
		fact.Committed.Type != session.TurnEndEventName {
		return
	}
	supervisor.mutex.Lock()
	parentDeliveries := supervisor.entries[fact.Conversation.ID()]
	targets := make([]*interactionDelivery, 0, len(parentDeliveries))
	for _, delivery := range parentDeliveries {
		targets = append(targets, delivery)
	}
	supervisor.mutex.Unlock()
	for _, delivery := range targets {
		delivery.Notify()
	}
}

func (supervisor *deliverySupervisor) agentDisposed(subject agent.Agent) {
	if supervisor == nil || subject == nil {
		return
	}
	supervisor.mutex.Lock()
	parentDeliveries := supervisor.entries[subject.ID()]
	targets := make([]*interactionDelivery, 0, len(parentDeliveries))
	for childID, delivery := range parentDeliveries {
		if !agent.Same(delivery.parent, subject) {
			continue
		}
		targets = append(targets, delivery)
		delete(parentDeliveries, childID)
	}
	if len(parentDeliveries) == 0 {
		delete(supervisor.entries, subject.ID())
	}
	supervisor.mutex.Unlock()
	for _, delivery := range targets {
		delivery.Stop()
	}
}

func (supervisor *deliverySupervisor) close() {
	if supervisor == nil {
		return
	}
	supervisor.mutex.Lock()
	supervisor.closing = true
	targets := make([]*interactionDelivery, 0)
	for _, parentDeliveries := range supervisor.entries {
		for _, delivery := range parentDeliveries {
			targets = append(targets, delivery)
		}
	}
	supervisor.entries = nil
	supervisor.mutex.Unlock()
	for _, delivery := range targets {
		delivery.Stop()
	}
}
