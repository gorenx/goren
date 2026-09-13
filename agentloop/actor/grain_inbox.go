package loopactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// executePending is an Inbox wakeup after the committing admission is confirmed.
type executePending struct{ creationID string }

// readActivity observes pending input and execution status without claiming work.
func (owner *Grain) readActivity(gc *goakt.GrainContext, input ReadActivity) {
	result := Activity{
		Err: owner.checkCreation(input.CreationID),
	}
	if result.Err == nil {
		result.Pending, result.Err = owner.business.Inbox()
		result.Status = owner.business.StatusValue()
	}
	gc.Response(result)
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
	wake := make(chan bool, 1)
	if proposal.Wake {
		creationID := owner.creationID
		stopped := owner.stopped
		err := gc.PipeToSelf(func() (any, error) {
			select {
			case accepted := <-wake:
				if accepted {
					return executePending{creationID: creationID}, nil
				}
				return nil, nil
			case <-stopped:
				return nil, nil
			}
		})
		if err != nil {
			reply.Response(Changed{Err: err})
			return
		}
	}
	statusContext := context.WithoutCancel(gc.Context())
	system := gc.ActorSystem()
	gc.RequestGrain(
		owner.conversation,
		sessionactor.Commit{
			CreationID: owner.creationID,
			Plan:       session.Batch(proposal.Edit.Drafts...),
		},
		goakt.WithRequestTimeout(0),
		goakt.WithReentrancyMode(reentrancy.StashNonReentrant),
	).Then(
		func(payload any, failure error) {
			previous := owner.business.StatusValue()
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
			if proposal.Wake {
				wake <- result.Err == nil
			}
			owner.refreshIdle()
			current := owner.business.StatusValue()
			if current != previous {
				publishStatus(statusContext, system, agent.StatusNotice{
					AgentID: owner.business.ID(),
					Status:  current,
				})
			}
			reply.Response(result)
		},
	)
}

func (owner *Grain) promote(gc *goakt.GrainContext, input Promote) {
	reply := owner.inboxReply(gc, input.CreationID)
	if reply == nil {
		return
	}
	proposal, err := owner.business.Promote(input.MessageID)
	owner.commitAdmission(gc, reply, proposal, err)
}

// Inbox commands propose mutations; Session commit acknowledgement precedes business confirmation.
func (owner *Grain) receiveInbox(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case executePending:
		if owner.checkCreation(input.creationID) == nil {
			err := owner.business.Run(owner.services(gc))
			if err != nil {
				owner.services(gc).Observer.ExecutionFailed(err)
			}
		}
	case ReadActivity:
		owner.readActivity(gc, input)
	case Followup:
		owner.followup(gc, input)
	case Steer:
		owner.steer(gc, input)
	case Inject:
		owner.inject(gc, input)
	case Replace:
		owner.replace(gc, input)
	case Promote:
		owner.promote(gc, input)
	case Remove:
		owner.remove(gc, input)
	default:
		return false
	}
	return true
}
