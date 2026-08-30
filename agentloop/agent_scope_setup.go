package agentloop

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

func (owner *agentScope) ApplySetup(
	requestContext context.Context,
	subject agent.Agent,
	setup agent.Setup,
) (agent.ScopeResources, error) {
	if owner == nil || subject == nil || setup == nil {
		return nil, errors.New("agentloop: Agent Scope setup is incomplete")
	}
	if requestContext == nil {
		return nil, errors.New("agentloop: Agent Scope setup Context is nil")
	}
	finishCall, err := owner.beginCall()
	if err != nil {
		return nil, err
	}
	defer finishCall()
	owner.setupMutex.Lock()
	defer owner.setupMutex.Unlock()
	draft := &scopeDraft{
		owner:   owner,
		subject: subject,
	}
	if err := setup.Apply(requestContext, subject, draft); err != nil {
		return nil, errors.Join(
			err,
			draft.rollback(context.WithoutCancel(requestContext)),
		)
	}
	resources, err := draft.commit()
	if err != nil {
		return nil, errors.Join(
			err,
			draft.rollback(context.WithoutCancel(requestContext)),
		)
	}
	owner.mutex.Lock()
	owner.resources = append(owner.resources, resources)
	owner.mutex.Unlock()
	return resources, nil
}

// scopeDraft owns uncommitted changes made by one Setup. It is private so
// commit validation cannot leak into extension-facing contracts.
type scopeDraft struct {
	owner     *agentScope
	subject   agent.Agent
	resources []agent.ScopeResource
	checks    []agent.ScopeCheck
	committed bool
}

func (draft *scopeDraft) ApplyNestedSetup(
	requestContext context.Context,
	setup agent.Setup,
) (agent.ScopeResources, error) {
	if setup == nil {
		return nil, errors.New("agentloop: nested Agent Setup is nil")
	}
	child := &scopeDraft{
		owner:   draft.owner,
		subject: draft.subject,
	}
	if err := setup.Apply(requestContext, draft.subject, child); err != nil {
		return nil, errors.Join(
			err,
			child.rollback(context.WithoutCancel(requestContext)),
		)
	}
	resources, err := child.commit()
	if err != nil {
		return nil, errors.Join(
			err,
			child.rollback(context.WithoutCancel(requestContext)),
		)
	}
	if err = draft.Own(resources); err != nil {
		return nil, errors.Join(
			err,
			resources.Close(context.WithoutCancel(requestContext)),
		)
	}
	return resources, nil
}

func (draft *scopeDraft) AddPromptSection(
	requestContext context.Context,
	section systemprompt.PromptSection,
) error {
	handle, err := draft.owner.prompts.AddSection(requestContext, section)
	if err != nil {
		return err
	}
	return draft.Own(promptResource{handle: handle})
}

func (draft *scopeDraft) AddPromptVariable(
	requestContext context.Context,
	name string,
	provider systemprompt.VariableProvider,
) error {
	handle, err := draft.owner.prompts.AddVariable(
		requestContext,
		name,
		provider,
	)
	if err != nil {
		return err
	}
	return draft.Own(promptResource{handle: handle})
}

func (draft *scopeDraft) UsePromptAssembly(
	middleware systemprompt.AssemblyMiddleware,
) error {
	handle, err := draft.owner.prompts.UseAssembly(middleware)
	if err != nil {
		return err
	}
	return draft.Own(handle)
}

func (draft *scopeDraft) SuppressRuntimeContext(
	requestContext context.Context,
	name string,
) error {
	handle, err := draft.owner.prompts.AddRuntimeContextSuppressor(
		requestContext,
		name,
	)
	if err != nil {
		return err
	}
	return draft.Own(promptResource{handle: handle})
}

func (draft *scopeDraft) AddTool(
	requestContext context.Context,
	definition tools.ToolDefinition,
) error {
	handle, err := draft.owner.tools.AddTool(requestContext, definition)
	if err != nil {
		return err
	}
	return draft.Own(toolResource{handle: handle})
}

func (draft *scopeDraft) AddToolRestriction(
	requestContext context.Context,
	name string,
	restriction tools.ToolRestriction,
) error {
	handle, err := draft.owner.tools.AddRestriction(
		requestContext,
		name,
		restriction,
	)
	if err != nil {
		return err
	}
	return draft.Own(restrictionResource{handle: handle})
}

func (draft *scopeDraft) AddToolGuard(
	requestContext context.Context,
	name string,
	guard tools.ToolGuard,
) error {
	handle, err := draft.owner.tools.AddGuard(requestContext, name, guard)
	if err != nil {
		return err
	}
	return draft.Own(guardResource{handle: handle})
}

func (draft *scopeDraft) UseToolExecution(
	middleware tools.ExecuteMiddleware,
) error {
	handle, err := draft.owner.tools.UseExecution(middleware)
	if err != nil {
		return err
	}
	return draft.Own(handle)
}

func (draft *scopeDraft) ObserveAgentEvents(
	observer agent.AgentEventObserver,
) error {
	handle, err := draft.owner.observeAgentEvents(observer)
	if err != nil {
		return err
	}
	return draft.Own(handle)
}

func (draft *scopeDraft) UsePreStep(middleware agent.PreStepMiddleware) error {
	handle, err := draft.owner.usePreStep(middleware)
	if err != nil {
		return err
	}
	return draft.Own(handle)
}

func (draft *scopeDraft) UseRequest(middleware agent.RequestMiddleware) error {
	handle, err := draft.owner.useRequest(middleware)
	if err != nil {
		return err
	}
	return draft.Own(handle)
}

func (draft *scopeDraft) UseRequestError(
	middleware agent.RequestErrorMiddleware,
) error {
	handle, err := draft.owner.useRequestError(middleware)
	if err != nil {
		return err
	}
	return draft.Own(handle)
}

func (draft *scopeDraft) ObserveToolResults(observer tools.ResultObserver) error {
	handle, err := draft.owner.tools.ObserveResults(observer)
	if err != nil {
		return err
	}
	return draft.Own(handle)
}

func (draft *scopeDraft) Own(resource agent.ScopeResource) error {
	if resource == nil {
		return errors.New("agentloop: Scope resource is nil")
	}
	draft.resources = append(draft.resources, resource)
	return nil
}

func (draft *scopeDraft) Check(scopeCheck agent.ScopeCheck) error {
	if scopeCheck == nil {
		return errors.New("agentloop: Scope check is nil")
	}
	draft.checks = append(draft.checks, scopeCheck)
	return nil
}

func (draft *scopeDraft) commit() (*scopeResources, error) {
	for _, scopeCheck := range draft.checks {
		if err := scopeCheck.Check(); err != nil {
			return nil, err
		}
	}
	draft.committed = true
	return &scopeResources{
		resources: append([]agent.ScopeResource(nil), draft.resources...),
	}, nil
}

func (draft *scopeDraft) rollback(closeContext context.Context) error {
	if draft == nil || draft.committed {
		return nil
	}
	return closeScopeResources(closeContext, draft.resources)
}

// scopeResources is the exact committed resource set created by one Setup.
type scopeResources struct {
	once      sync.Once
	resources []agent.ScopeResource
	err       error
}

func (resources *scopeResources) Close(closeContext context.Context) error {
	if resources == nil {
		return nil
	}
	resources.once.Do(func() {
		resources.err = closeScopeResources(closeContext, resources.resources)
		resources.resources = nil
	})
	return resources.err
}

func closeScopeResources(
	closeContext context.Context,
	resources []agent.ScopeResource,
) error {
	var closeErr error
	for index := len(resources) - 1; index >= 0; index-- {
		closeErr = errors.Join(closeErr, resources[index].Close(closeContext))
	}
	return closeErr
}

type promptResource struct{ handle *systemprompt.PromptHandle }

func (resource promptResource) Close(closeContext context.Context) error {
	return resource.handle.Unregister(closeContext)
}

type toolResource struct{ handle *tools.ToolHandle }

func (resource toolResource) Close(closeContext context.Context) error {
	return resource.handle.Unregister(closeContext)
}

type restrictionResource struct{ handle *tools.RestrictionHandle }

func (resource restrictionResource) Close(closeContext context.Context) error {
	return resource.handle.Unregister(closeContext)
}

type guardResource struct{ handle *tools.GuardHandle }

func (resource guardResource) Close(closeContext context.Context) error {
	return resource.handle.Unregister(closeContext)
}

var _ agent.ScopeEditor = (*scopeDraft)(nil)
var _ agent.ScopeResources = (*scopeResources)(nil)
