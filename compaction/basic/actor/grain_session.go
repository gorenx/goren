package compactionactor

import (
	"errors"

	"github.com/gorenx/goren/compaction/basic"
	"github.com/gorenx/goren/llm/tokenmeter"
	meteractor "github.com/gorenx/goren/llm/tokenmeter/actor"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func (owner *Grain) request(gc *goakt.GrainContext, current *invocation, target *goakt.GrainIdentity, message any, accept func(any) bool) {
	gc.RequestGrain(target, message, goakt.WithRequestTimeout(owner.timeout), goakt.WithReentrancyMode(reentrancy.AllowAll)).Then(func(payload any, failure error) {
		if owner.active != current {
			return
		}
		if failure == nil && accept(payload) {
			return
		}
		owner.later(gc, current, func(next *goakt.GrainContext) {
			owner.request(next, current, target, message, accept)
		})
	})
}

func (owner *Grain) read(gc *goakt.GrainContext, current *invocation, accept func(session.Snapshot) bool) {
	owner.request(gc, current, owner.conversation, sessionactor.Read{
		CreationID: owner.creationID,
	}, func(payload any) bool {
		view, ok := payload.(sessionactor.View)
		if !ok || view.Err != nil {
			return false
		}
		return accept(view.Snapshot)
	})
}

func (owner *Grain) measure(gc *goakt.GrainContext, current *invocation, accept func(tokenmeter.Measurement, error)) {
	owner.request(gc, current, owner.meter, meteractor.Measure{
		SessionID:  owner.agentID,
		CreationID: owner.creationID,
		Events:     current.snapshot.Events,
	}, func(payload any) bool {
		measured, ok := payload.(meteractor.Measured)
		if !ok {
			return false
		}
		accept(measured.Value, measured.Err)
		return true
	})
}

func (owner *Grain) prepare(gc *goakt.GrainContext, current *invocation) {
	owner.read(gc, current, func(snapshot session.Snapshot) bool {
		current.snapshot = snapshot
		owner.measure(gc, current, func(prices tokenmeter.Measurement, failure error) {
			if failure != nil {
				owner.reject(gc, current, failure)
				return
			}
			current.prices = prices
			target, err := owner.business.CapacityTarget(current.input.Mode, snapshot)
			if err != nil {
				owner.reject(gc, current, err)
				return
			}
			if target == nil {
				owner.begin(gc, current, nil)
				return
			}
			owner.models.ResolveModel(gc, target.Provider, target.Model, owner)
		})
		return true
	})
}

func (owner *Grain) begin(gc *goakt.GrainContext, current *invocation, capacity *int) {
	if err := owner.business.Begin(basic.Invocation{
		Mode:            current.input.Mode,
		Snapshot:        current.snapshot,
		Measurement:     current.prices,
		ContextWindow:   capacity,
		Fallback:        current.input.Fallback,
		SourceCommandID: current.input.SourceCommandID,
		Prune:           owner.pruner != nil,
		Cancellation:    current.cancelled,
	}); err != nil {
		owner.reject(gc, current, err)
		return
	}
	owner.advance(gc, current)
}

func (owner *Grain) advance(gc *goakt.GrainContext, current *invocation) {
	switch owner.business.Stage() {
	case basic.CompactionFinished:
		outcome, failure := owner.business.Result()
		owner.finish(gc, current, outcome, failure)
	case basic.PrunePending:
		owner.prune(gc, current)
	case basic.MeasurementPending:
		owner.read(gc, current, func(snapshot session.Snapshot) bool {
			current.snapshot = snapshot
			owner.measure(gc, current, func(prices tokenmeter.Measurement, failure error) {
				if failure != nil {
					_ = owner.business.RejectMeasurement(failure)
				} else {
					if err := owner.business.Measured(snapshot, prices); err != nil {
						_ = owner.business.RejectMeasurement(err)
					}
				}
				owner.advance(gc, current)
			})
			return true
		})
	case basic.TransactionPending:
		owner.transact(gc, current)
	}
}

func (owner *Grain) prune(gc *goakt.GrainContext, current *invocation) {
	proposal, err := owner.pruner.NewPlan(current.snapshot, &tokenmeter.TokenMeter{})
	if err != nil {
		if owner.business.Pruned(current.snapshot, err) == nil {
			owner.advance(gc, current)
		}
		return
	}
	owner.request(gc, current, owner.conversation, sessionactor.Commit{
		CreationID: owner.creationID,
		Plan:       proposal,
	}, func(payload any) bool {
		written, ok := payload.(sessionactor.Committed)
		if !ok || (written.Err != nil && len(written.Result.Events) != 0) {
			return false
		}
		owner.read(gc, current, func(snapshot session.Snapshot) bool {
			if err := owner.business.Pruned(snapshot, written.Err); err != nil {
				return false
			}
			current.snapshot = snapshot
			owner.advance(gc, current)
			return true
		})
		return true
	})
}

func (owner *Grain) transact(gc *goakt.GrainContext, current *invocation) {
	execution, err := owner.business.CurrentTransaction()
	if err != nil {
		gc.Err(err)
		return
	}
	switch execution.Stage() {
	case basic.StartPending, basic.CommitPending:
		proposal, err := execution.Write()
		if err != nil {
			gc.Err(err)
			return
		}
		owner.request(gc, current, owner.conversation, sessionactor.Commit{
			CreationID: owner.creationID,
			Plan:       proposal,
		}, func(payload any) bool {
			written, ok := payload.(sessionactor.Committed)
			if !ok || execution.AcceptCommit(written.Result, written.Err) != nil {
				return false
			}
			owner.advance(gc, current)
			return true
		})
	case basic.SummaryPending, basic.SummaryStreaming, basic.SummaryClosing:
		owner.generate(gc, current, execution)
	case basic.FlushPending:
		barrier, err := execution.FlushBarrier()
		if err != nil {
			gc.Err(err)
			return
		}
		owner.request(gc, current, owner.conversation, sessionactor.Flush{
			CreationID: owner.creationID,
			Barrier:    barrier,
		}, func(payload any) bool {
			durable, ok := payload.(sessionactor.Flushed)
			if !ok || execution.ConfirmFlush(durable.Barrier, durable.Err) != nil {
				return false
			}
			owner.advance(gc, current)
			return true
		})
	case basic.TransactionFinished:
		owner.read(gc, current, func(snapshot session.Snapshot) bool {
			if err := owner.business.CompleteTransaction(snapshot); err != nil {
				return false
			}
			current.callID = ""
			current.snapshot = snapshot
			owner.advance(gc, current)
			return true
		})
	default:
		gc.Err(errors.New("compaction: unexpected transaction scheduling stage"))
	}
}
