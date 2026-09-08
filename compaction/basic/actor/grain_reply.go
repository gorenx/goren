package compactionactor

import (
	"github.com/gorenx/goren/llm/tokenmeter"
	meteractor "github.com/gorenx/goren/llm/tokenmeter/actor"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// replyPurpose identifies the continuation of one dependency request, not a
// second business state machine. Retries keep the original request and purpose.
type replyPurpose uint8

const (
	preparationHistory replyPurpose = iota
	preparationTokens
	measurementHistory
	measurementTokens
	pruningCommit
	pruningHistory
	transactionCommit
	transactionFlush
	transactionHistory
	recoveryHistory
)

func (owner *Grain) acceptReply(gc *goakt.GrainContext, pending *exchange, payload any) bool {
	current := pending.invocation
	switch pending.purpose {
	case preparationHistory, measurementHistory, pruningHistory, transactionHistory, recoveryHistory:
		result, ok := payload.(sessionactor.View)
		if !ok || result.Err != nil {
			return false
		}
		return owner.acceptHistory(gc, current, pending.purpose, result.Snapshot)
	case preparationTokens, measurementTokens:
		result, ok := payload.(meteractor.Measured)
		if !ok {
			return false
		}
		owner.acceptMeasurement(gc, current, pending.purpose, result.Value, result.Err)
		return true
	case pruningCommit:
		result, ok := payload.(sessionactor.Committed)
		if !ok || (result.Err != nil && len(result.Result.Events) != 0) {
			return false
		}
		current.pruneError = result.Err
		owner.request(gc, current, owner.conversation, sessionactor.Read{
			CreationID: owner.creationID,
		}, pruningHistory)
		return true
	case transactionCommit:
		result, ok := payload.(sessionactor.Committed)
		if !ok {
			return false
		}
		execution, err := owner.business.CurrentTransaction()
		if err != nil || execution.AcceptCommit(result.Result, result.Err) != nil {
			return false
		}
		owner.advance(gc, current)
		return true
	case transactionFlush:
		result, ok := payload.(sessionactor.Flushed)
		if !ok {
			return false
		}
		execution, err := owner.business.CurrentTransaction()
		if err != nil || execution.ConfirmFlush(result.Barrier, result.Err) != nil {
			return false
		}
		owner.advance(gc, current)
		return true
	}
	return false
}

func (owner *Grain) acceptHistory(gc *goakt.GrainContext, current *invocation, purpose replyPurpose, snapshot session.Snapshot) bool {
	switch purpose {
	case preparationHistory, measurementHistory:
		current.snapshot = snapshot
		next := preparationTokens
		if purpose == measurementHistory {
			next = measurementTokens
		}
		owner.request(gc, current, owner.meter, meteractor.Measure{
			SessionID:  owner.agentID,
			CreationID: owner.creationID,
			Events:     snapshot.Events,
		}, next)
	case pruningHistory:
		if err := owner.business.Pruned(snapshot, current.pruneError); err != nil {
			return false
		}
		current.pruneError = nil
		current.snapshot = snapshot
		owner.advance(gc, current)
	case transactionHistory:
		if err := owner.business.CompleteTransaction(snapshot); err != nil {
			return false
		}
		current.callID = ""
		current.snapshot = snapshot
		owner.advance(gc, current)
	case recoveryHistory:
		approved, err := owner.recovery.Prepare(current.failure, snapshot)
		if err != nil || !approved {
			owner.report(gc, err)
			owner.continueHook(gc, current)
		} else {
			owner.prepare(gc, current)
		}
	}
	return true
}

func (owner *Grain) acceptMeasurement(gc *goakt.GrainContext, current *invocation, purpose replyPurpose, prices tokenmeter.Measurement, failure error) {
	if purpose == measurementTokens {
		if failure != nil {
			_ = owner.business.RejectMeasurement(failure)
		} else if err := owner.business.Measured(current.snapshot, prices); err != nil {
			_ = owner.business.RejectMeasurement(err)
		}
		owner.advance(gc, current)
		return
	}
	if failure != nil {
		owner.reject(gc, current, failure)
		return
	}
	current.prices = prices
	target, err := owner.business.CapacityTarget(current.input.Mode, current.snapshot)
	if err != nil {
		owner.reject(gc, current, err)
		return
	}
	if target == nil {
		owner.begin(gc, current, nil)
		return
	}
	owner.models.ResolveModel(gc, target.Provider, target.Model, owner)
}
