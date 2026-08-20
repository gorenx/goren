package plugin

import (
	"context"
	"errors"
)

func (runtimeEngine *Runtime) activateMountBatch(
	requestContext context.Context,
	mounts []*pluginMount,
) error {
	mainErr := runtimeEngine.activations.converge(
		requestContext,
		ActivationMain,
	)
	mainReadinessErr := runtimeEngine.dependencies.readiness(
		mounts,
		ActivationMain,
	)
	if err := errors.Join(mainErr, mainReadinessErr); err != nil {
		return err
	}
	commitErr := runtimeEngine.activations.converge(
		requestContext,
		ActivationCommit,
	)
	commitReadinessErr := runtimeEngine.dependencies.readiness(
		mounts,
		ActivationCommit,
	)
	return errors.Join(commitErr, commitReadinessErr)
}

func (runtimeEngine *Runtime) reconcile(requestContext context.Context) error {
	mainErr := runtimeEngine.activations.converge(
		requestContext,
		ActivationMain,
	)
	if mainErr != nil {
		return mainErr
	}
	return runtimeEngine.activations.converge(
		requestContext,
		ActivationCommit,
	)
}

func (runtimeEngine *Runtime) discardMountRoots(roots []*pluginMount) {
	runtimeEngine.mounts.deleteRoots(roots)
}
