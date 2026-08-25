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

func (router *runtimeContextRouter) register(
	conversation session.Context,
	projection *runtimeContextProjection,
) (*runtimeContextRegistration, error) {
	if conversation == nil || projection == nil {
		return nil, errors.New(
			"agentloop: runtime-context Session and projection are required",
		)
	}
	router.mutex.Lock()
	defer router.mutex.Unlock()
	if _, exists := router.projections[conversation]; exists {
		return nil, fmt.Errorf(
			"agentloop: Session %q already has a runtime-context projection",
			conversation.ID(),
		)
	}
	router.projections[conversation] = projection
	return &runtimeContextRegistration{
		router:       router,
		conversation: conversation,
		projection:   projection,
	}, nil
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

type runtimeContextRegistration struct {
	once         sync.Once
	router       *runtimeContextRouter
	conversation session.Context
	projection   *runtimeContextProjection
}

func newRuntimeContextRouter() *runtimeContextRouter {
	return &runtimeContextRouter{
		projections: make(
			map[session.Context]*runtimeContextProjection,
		),
	}
}

func (registration *runtimeContextRegistration) Release() {
	if registration == nil {
		return
	}
	registration.once.Do(func() {
		router := registration.router
		if router == nil || registration.conversation == nil ||
			registration.projection == nil {
			return
		}
		router.mutex.Lock()
		if router.projections[registration.conversation] == registration.projection {
			delete(router.projections, registration.conversation)
		}
		router.mutex.Unlock()
	})
}
