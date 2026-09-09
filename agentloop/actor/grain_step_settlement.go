package loopactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// finishStep carries the conclusion selected by the business Agent.
type finishStep struct{ ending agentloop.EndStep }

// stepFinished distinguishes persistence failure from the retained execution error.
type stepFinished struct {
	failure error
	err     error
}

func (owner *Grain) settleStep(gc *goakt.GrainContext, input finishStep) {
	if owner.conversation == nil ||
		owner.model != nil ||
		owner.preparation != nil ||
		owner.stopping != nil ||
		owner.ending != nil {
		gc.Response(stepFinished{err: errors.New("agentloop: Step settlement requires released execution resources")})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(stepFinished{err: errors.New("agentloop: Step settlement requires an asynchronous request")})
		return
	}
	gc.RequestGrain(owner.conversation, sessionactor.Commit{
		CreationID: owner.creationID,
		Plan:       stepEndWrite{ending: input.ending},
	}, goakt.WithRequestTimeout(0), goakt.WithReentrancyMode(reentrancy.StashNonReentrant)).Then(func(payload any, failure error) {
		if failure == nil {
			committed, ok := payload.(sessionactor.Committed)
			if !ok {
				failure = errors.New("agentloop: invalid Step end acknowledgement")
			} else if committed.Err != nil {
				failure = committed.Err
			} else {
				failure = owner.business.ConfirmStepEnd(committed.Result)
			}
		}
		reply.Response(stepFinished{
			failure: input.ending.Err,
			err:     failure,
		})
	})
}

// stepEndWrite evaluates an immutable conclusion inside the Session owner.
type stepEndWrite struct{ ending agentloop.EndStep }

func (plan stepEndWrite) Build(_ context.Context, snapshot session.Snapshot) ([]session.EventDraft, error) {
	return plan.ending.Drafts(snapshot)
}

func (plan stepEndWrite) FindCommit(snapshot session.Snapshot) (*session.WriteResult, error) {
	return plan.ending.Find(snapshot)
}
