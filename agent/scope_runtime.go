package agent

import "context"

// AgentScopeRuntime is the consumer-owned port from Agent business behavior
// and LifecycleCoordinator to the runtime Scope of one exact Agent epoch.
// Plugin topology and registration identities remain adapter details.
type AgentScopeRuntime interface {
	Dispatch(context.Context, RuntimeEvent) error
	ResolvePreStep(
		context.Context,
		PreStepNotice,
		PreStepAction,
	) (PreStepDecision, error)
	ResolveRequest(
		context.Context,
		RequestNotice,
		RequestAction,
	) (RequestResolution, error)
	ResolveRequestError(
		context.Context,
		RequestErrorNotice,
		RequestErrorHandler,
	) (RequestErrorAction, error)
	Provision(context.Context, Provisioner) error
	Teardown(context.Context) error
}
