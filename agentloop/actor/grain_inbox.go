package loopactor

import (
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func (owner *Grain) readInbox(gc *goakt.GrainContext) {
	pending, err := owner.business.Inbox()
	gc.Response(Inbox{
		NextTurn: pending.NextTurn,
		NextStep: pending.NextStep,
		Err:      err,
	})
}

func (owner *Grain) followup(gc *goakt.GrainContext, input Followup) {
	reply := owner.inboxReply(gc, input.CreationID)
	if reply == nil {
		return
	}
	proposal, err := owner.business.Followup(input.Message)
	owner.commitAdmission(gc, reply, proposal, err)
}

func (owner *Grain) steer(gc *goakt.GrainContext, input Steer) {
	reply := owner.inboxReply(gc, input.CreationID)
	if reply == nil {
		return
	}
	proposal, err := owner.business.Steer(input.Message)
	owner.commitAdmission(gc, reply, proposal, err)
}

func (owner *Grain) inject(gc *goakt.GrainContext, input Inject) {
	reply := owner.inboxReply(gc, input.CreationID)
	if reply == nil {
		return
	}
	proposal, err := owner.business.Inject(input.Message)
	owner.commitAdmission(gc, reply, proposal, err)
}

func (owner *Grain) replace(gc *goakt.GrainContext, input Replace) {
	reply := owner.inboxReply(gc, input.CreationID)
	if reply == nil {
		return
	}
	edit, err := owner.business.Replace(input.MessageID, input.Message)
	owner.commitAdmission(gc, reply, agent.Admission{
		Edit: edit,
	}, err)
}

func (owner *Grain) remove(gc *goakt.GrainContext, input Remove) {
	reply := owner.inboxReply(gc, input.CreationID)
	if reply == nil {
		return
	}
	edit, err := owner.business.Remove(input.MessageID)
	owner.commitAdmission(gc, reply, agent.Admission{
		Edit: edit,
	}, err)
}

// inboxReply validates transport admission before any business operation runs.
func (owner *Grain) inboxReply(gc *goakt.GrainContext, creationID string) *goakt.GrainReply {
	if err := owner.checkCreation(creationID); err != nil {
		gc.Response(Changed{
			Err: err,
		})
		return nil
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(Changed{
			Err: errors.New("agentloop: Inbox mutation requires an asynchronous request"),
		})
	}
	return reply
}

func (owner *Grain) commitAdmission(gc *goakt.GrainContext, reply *goakt.GrainReply, proposal agent.Admission, failure error) {
	if failure != nil {
		reply.Response(Changed{
			Err: failure,
		})
		return
	}
	owner.commitInbox(gc, reply, proposal, nil)
}

func (owner *Grain) commitInbox(gc *goakt.GrainContext, reply *goakt.GrainReply, proposal agent.Admission, deliveryErr error) {
	gc.RequestGrain(owner.conversation, sessionactor.Commit{
		CreationID: owner.creationID,
		Plan:       session.Batch(proposal.Edit.Drafts...),
	}, goakt.WithRequestTimeout(0), goakt.WithReentrancyMode(reentrancy.StashNonReentrant)).Then(func(payload any, failure error) {
		result := Changed{
			Err: failure,
		}
		if failure == nil {
			committed, ok := payload.(sessionactor.Committed)
			switch {
			case !ok:
				result.Err = errors.New("agentloop: invalid Session commit response")
			case committed.Err != nil:
				result.Err = committed.Err
			default:
				result.Changes, result.Err = owner.business.ConfirmAdmission(proposal, committed.Result)
			}
		}
		result.Err = errors.Join(deliveryErr, result.Err)
		owner.refreshIdle()
		reply.Response(result)
	})
}
