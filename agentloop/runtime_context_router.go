package agentloop

import (
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/session"
)

// runtimeContextRouter routes root Session Events by exact Session instance.
// It is a private collaborator of Plugin, not another runtime Plugin or
// Service.
type runtimeContextRouter struct {
	mutex       sync.RWMutex
	projections map[session.Context]*runtimeContextProjection
}

func newRuntimeContextRouter() *runtimeContextRouter {
	return &runtimeContextRouter{
		projections: make(
			map[session.Context]*runtimeContextProjection,
		),
	}
}

func (router *runtimeContextRouter) register(
	conversation session.Context,
	projection *runtimeContextProjection,
) error {
	if conversation == nil || projection == nil {
		return errors.New(
			"agentloop: runtime-context Session and projection are required",
		)
	}
	router.mutex.Lock()
	defer router.mutex.Unlock()
	if _, exists := router.projections[conversation]; exists {
		return fmt.Errorf(
			"agentloop: Session %q already has a runtime-context projection",
			conversation.ID(),
		)
	}
	router.projections[conversation] = projection
	return nil
}

func (router *runtimeContextRouter) remove(
	conversation session.Context,
	projection *runtimeContextProjection,
) {
	if conversation == nil || projection == nil {
		return
	}
	router.mutex.Lock()
	if router.projections[conversation] == projection {
		delete(router.projections, conversation)
	}
	router.mutex.Unlock()
}

func (router *runtimeContextRouter) accept(
	appended session.EventAppended,
) {
	if appended.Conversation == nil {
		return
	}
	router.mutex.RLock()
	projection := router.projections[appended.Conversation]
	router.mutex.RUnlock()
	if projection != nil {
		projection.accept(appended.Committed)
	}
}

func (router *runtimeContextRouter) clear() int {
	router.mutex.Lock()
	dangling := len(router.projections)
	router.projections = make(
		map[session.Context]*runtimeContextProjection,
	)
	router.mutex.Unlock()
	return dangling
}
