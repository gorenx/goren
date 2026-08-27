package bound

import (
	"context"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
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
