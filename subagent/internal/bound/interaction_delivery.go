package bound

import (
	"context"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
)

type interactionDelivery struct {
	owner    *deliverySupervisor
	key      bindingKey
	parent   agent.Agent
	slot     *bindingSlot
	floor    int64
	dirty    chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

func newInteractionDelivery(
	owner *deliverySupervisor,
	parentAgent agent.Agent,
	binding subagentprojection.BoundBinding,
	childSession session.Context,
	slot *bindingSlot,
) *interactionDelivery {
	workerContext, cancelWorker := context.WithCancel(context.Background())
	floor := binding.Seq + 1
	if seedLength := childSession.Header().SeedLength; seedLength != nil &&
		*seedLength > floor {
		floor = *seedLength
	}
	return &interactionDelivery{
		owner: owner,
		key: bindingKey{
			parentID: parentAgent.ID(),
			childID:  binding.ChildSessionID,
		},
		parent: parentAgent,
		slot:   slot,
		floor:  floor,
		dirty:  make(chan struct{}, 1),
		ctx:    workerContext,
		cancel: cancelWorker,
		done:   make(chan struct{}),
	}
}

func (delivery *interactionDelivery) Notify() {
	select {
	case delivery.dirty <- struct{}{}:
	default:
	}
}

func (delivery *interactionDelivery) Run() {
	defer close(delivery.done)
	for {
		select {
		case <-delivery.ctx.Done():
			return
		case <-delivery.dirty:
			if err := delivery.catchUp(); err != nil {
				delivery.reportFailure(err)
			}
		}
	}
}

func (delivery *interactionDelivery) Stop() {
	delivery.stopOnce.Do(delivery.cancel)
	<-delivery.done
}

func (delivery *interactionDelivery) reportFailure(err error) {
	if delivery.owner.failures == nil || err == nil {
		return
	}
	delivery.owner.failures.ReportBoundInteractionFailure(
		InteractionFailure{
			ParentID: delivery.key.parentID,
			ChildID:  delivery.key.childID,
			Error:    err,
		},
	)
}
