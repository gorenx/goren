package plugin

import (
	"context"
	"errors"
)

func (runtimeEngine *Runtime) activateMountBatch(
	requestContext context.Context,
	admitted []*pluginMount,
	topology []*pluginMount,
) error {
	mainErr := runtimeEngine.activations.converge(
		requestContext,
		ActivationMain,
		topology,
	)
	mainReadinessErr := runtimeEngine.dependencies.readiness(
		admitted,
		ActivationMain,
	)
	if err := errors.Join(mainErr, mainReadinessErr); err != nil {
		return err
	}
	commitErr := runtimeEngine.activations.converge(
		requestContext,
		ActivationCommit,
		topology,
	)
	commitReadinessErr := runtimeEngine.dependencies.readiness(
		admitted,
		ActivationCommit,
	)
	return errors.Join(commitErr, commitReadinessErr)
}

func (runtimeEngine *Runtime) reconcile(requestContext context.Context) error {
	topology := runtimeEngine.mounts.all()
	mainErr := runtimeEngine.activations.converge(
		requestContext,
		ActivationMain,
		topology,
	)
	if mainErr != nil {
		return mainErr
	}
	return runtimeEngine.activations.converge(
		requestContext,
		ActivationCommit,
		topology,
	)
}

func (runtimeEngine *Runtime) discardMountRoots(roots []*pluginMount) {
	runtimeEngine.mounts.deleteRoots(roots)
}
