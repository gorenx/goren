package compactionactor

import (
	"errors"

	"github.com/gorenx/goren/compaction/basic"
	"github.com/gorenx/goren/llm/tokenmeter"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func (owner *Grain) prepare(gc *goakt.GrainContext, current *invocation) {
	owner.request(gc, current, owner.conversation, sessionactor.Read{
		CreationID: owner.creationID,
	}, preparationHistory)
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
		owner.request(gc, current, owner.conversation, sessionactor.Read{
			CreationID: owner.creationID,
		}, measurementHistory)
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
	}, pruningCommit)
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
		}, transactionCommit)
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
		}, transactionFlush)
	case basic.TransactionFinished:
		owner.request(gc, current, owner.conversation, sessionactor.Read{
			CreationID: owner.creationID,
		}, transactionHistory)
	default:
		gc.Err(errors.New("compaction: unexpected transaction scheduling stage"))
	}
}
