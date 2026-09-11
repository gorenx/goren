package compactionactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/compaction/basic"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func (owner *Grain) beginMaintenance(gc *goakt.GrainContext, current *invocation) {
	system := gc.ActorSystem()
	ctx := context.WithoutCancel(gc.Context())
	timeout := owner.timeout
	target := current.input.Agent
	message := agentactor.BeginMaintenance{
		CreationID: owner.creationID,
		RequestID:  current.input.RequestID,
		Worker:     gc.Self(),
	}
	err := gc.PipeToSelf(
		func() (any, error) {
			payload, failure := system.AskGrain(ctx, target, message, timeout)
			return continuation{
				call: current,
				run: func(gc *goakt.GrainContext) {
					if owner.active != current {
						return
					}
					accepted, ok := payload.(agentactor.MaintenanceAccepted)
					if failure != nil || !ok {
						current.reply.Err(errors.Join(failure, errors.New("compaction: maintenance admission is unconfirmed")))
						return
					}
					if accepted.Err != nil {
						owner.active = nil
						failure := accepted.Err
						if errors.Is(failure, agent.ErrBusy) {
							failure = &compaction.ManualError{
								Code:  compaction.ManualErrorBusy,
								Cause: failure,
							}
						}
						current.reply.Response(Result{
							Err: failure,
						})
						return
					}
					owner.prepare(gc, current)
				},
			}, nil
		},
	)
	if err != nil {
		current.reply.Err(err)
	}
}

func (owner *Grain) finishMaintenance(gc *goakt.GrainContext, current *invocation, outcome basic.Outcome, problem error) {
	system := gc.ActorSystem()
	ctx := context.WithoutCancel(gc.Context())
	timeout := owner.timeout
	target := current.input.Agent
	message := agentactor.FinishMaintenance{
		CreationID: owner.creationID,
		RequestID:  current.input.RequestID,
	}

	err := gc.PipeToSelf(
		func() (any, error) {
			payload, failure := system.AskGrain(ctx, target, message, timeout)
			return continuation{
				call: current,
				run: func(gc *goakt.GrainContext) {
					if owner.active != current {
						return
					}
					finished, ok := payload.(agentactor.MaintenanceFinished)
					if failure != nil || !ok || finished.Err != nil {
						current.reply.Err(errors.Join(failure, finished.Err, errors.New("compaction: maintenance release is unconfirmed")))
						return
					}
					owner.active = nil
					current.reply.Response(Result{
						Outcome: outcome,
						Err:     problem,
					})
				},
			}, nil
		},
	)
	if err != nil {
		current.reply.Err(err)
	}
}

func (owner *Grain) cancelMaintenance(gc *goakt.GrainContext, input agentactor.CancelMaintenance) {
	current := owner.active
	if current == nil || current.hook != nil || current.input.RequestID != input.RequestID {
		gc.Response(agentactor.MaintenanceCancelled{})
		return
	}
	gc.Response(agentactor.MaintenanceCancelled{
		Err: owner.cancelInvocation(gc, current, input.Cause),
	})
}
