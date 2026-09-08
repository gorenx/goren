package llmactor

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/llm"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type metadataTask struct {
	recipient *goakt.GrainIdentity
	cancel    context.CancelCauseFunc
}

type queryCompleted struct {
	id      RequestID
	task    *metadataTask
	message any
}

func (worker *Worker) query(rc *goakt.ReceiveContext, route Query, command any) {
	var failure error
	if route.RequestID == "" || route.ReplyTo == nil || route.Deadline.IsZero() {
		failure = errors.New("llm: metadata query requires identity, recipient and deadline")
	}
	if failure == nil && worker.routes.LLM == nil {
		failure = errors.New("llm: worker is not initialized")
	}
	if failure == nil && worker.queries[route.RequestID] != nil {
		failure = errors.New("llm: metadata query identity is already active")
	}
	if failure != nil {
		worker.reply(rc, route.ReplyTo, queryFailure(command, route.RequestID, failure))
		return
	}
	deadline, releaseDeadline := context.WithDeadline(worker.lifetime, route.Deadline)
	operation, cancel := context.WithCancelCause(deadline)
	task := &metadataTask{
		recipient: route.ReplyTo,
		cancel:    cancel,
	}
	worker.queries[route.RequestID] = task
	system := rc.ActorSystem()
	registry := worker.routes.LLM
	if err := rc.Self().PipeTo(context.WithoutCancel(rc.Context()), rc.Self(), func() (any, error) {
		defer releaseDeadline()
		result := executeQuery(operation, system, registry, command, operation.Err())
		return queryCompleted{
			id:      route.RequestID,
			task:    task,
			message: result,
		}, nil
	}); err != nil {
		cancel(err)
		releaseDeadline()
		delete(worker.queries, route.RequestID)
		worker.reply(rc, route.ReplyTo, queryFailure(command, route.RequestID, err))
	}
}

func (worker *Worker) queryResult(rc *goakt.ReceiveContext, result queryCompleted) {
	if worker.queries[result.id] != result.task {
		return
	}
	delete(worker.queries, result.id)
	result.task.cancel(nil)
	worker.reply(rc, result.task.recipient, result.message)
}

func (worker *Worker) cancelQuery(input CancelQuery) {
	if task := worker.queries[input.RequestID]; task != nil {
		task.cancel(input.Cause)
	}
}

func executeQuery(ctx context.Context, system goakt.ActorSystem, registry *goakt.GrainIdentity, command any, failure error) any {
	switch input := command.(type) {
	case ListModels:
		var entries []llm.ModelInfo
		if failure == nil {
			selected, err := queryBinding(ctx, system, registry, input.Provider)
			failure = err
			if failure == nil {
				entries, failure = selected.ListModels(ctx)
			}
		}
		return ModelsListed{
			RequestID: input.RequestID,
			Models:    entries,
			Err:       errors.Join(context.Cause(ctx), failure),
		}
	case ResolveModel:
		var info llm.ResolvedModelInfo
		if failure == nil {
			selected, err := queryBinding(ctx, system, registry, input.Provider)
			failure = err
			if failure == nil {
				info, failure = selected.ResolveModelInfo(ctx, input.Model)
			}
		}
		return ModelResolved{
			RequestID: input.RequestID,
			Info:      info,
			Err:       errors.Join(context.Cause(ctx), failure),
		}
	case ResolveConfig:
		var settings llm.CallConfig
		if failure == nil {
			selected, err := queryBinding(ctx, system, registry, input.Config.Provider)
			failure = err
			if failure == nil {
				settings, failure = selected.ResolveCallConfig(ctx, input.Config)
			}
		}
		return ConfigResolved{
			RequestID: input.RequestID,
			Config:    settings,
			Err:       errors.Join(context.Cause(ctx), failure),
		}
	default:
		return nil
	}
}

func queryBinding(ctx context.Context, system goakt.ActorSystem, registry *goakt.GrainIdentity, provider string) (*llm.Binding, error) {
	payload, err := system.AskGrain(ctx, registry, Select{
		Provider: provider,
	}, queryTimeout(ctx))
	if err != nil {
		return nil, err
	}
	selected, ok := payload.(Selected)
	if !ok {
		return nil, errors.New("llm: invalid provider binding result")
	}
	if selected.Err != nil {
		return nil, selected.Err
	}
	if selected.Binding == nil {
		return nil, errors.New("llm: provider binding is missing")
	}
	return selected.Binding, nil
}

func queryTimeout(ctx context.Context) time.Duration {
	deadline, _ := ctx.Deadline()
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return time.Nanosecond
	}
	return remaining
}
