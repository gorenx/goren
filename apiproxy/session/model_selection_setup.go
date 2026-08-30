package sessionapi

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
)

// selectionSetup binds API model-selection state to one Agent Scope without
// introducing an Agent-local Plugin.
type selectionSetup struct {
	owner      *AgentSessions
	selection  *agent.ModelSelectionRef
	middleware agent.Setup
}

func newSelectionSetup(
	owner *AgentSessions,
	selectionRef *agent.ModelSelectionRef,
) (*selectionSetup, error) {
	if owner == nil || selectionRef == nil {
		return nil, errors.New(
			"apiproxy/session: model selection owner and reference are required",
		)
	}
	middleware, err := agent.NewModelSelectionSetup(selectionRef)
	if err != nil {
		return nil, err
	}
	return &selectionSetup{
		owner:      owner,
		selection:  selectionRef,
		middleware: middleware,
	}, nil
}

func (setup *selectionSetup) Apply(
	requestContext context.Context,
	subject agent.Agent,
	editor agent.ScopeEditor,
) error {
	lease, err := setup.owner.installSelection(subject, setup.selection)
	if err != nil {
		return err
	}
	if err = editor.Own(lease); err != nil {
		return err
	}
	return setup.middleware.Apply(requestContext, subject, editor)
}

// selectionLease marks one installed map entry inactive during Scope close.
// It does not point back to AgentSessions, so Scope ownership remains a DAG.
type selectionLease struct {
	once   sync.Once
	mutex  sync.RWMutex
	active bool
}

func newSelectionLease() *selectionLease {
	return &selectionLease{
		active: true,
	}
}

func (lease *selectionLease) Close(context.Context) error {
	if lease == nil {
		return nil
	}
	lease.once.Do(func() {
		lease.mutex.Lock()
		lease.active = false
		lease.mutex.Unlock()
	})
	return nil
}

func (lease *selectionLease) isActive() bool {
	lease.mutex.RLock()
	active := lease.active
	lease.mutex.RUnlock()
	return active
}

var _ agent.Setup = (*selectionSetup)(nil)
var _ agent.ScopeResource = (*selectionLease)(nil)
