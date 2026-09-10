package toolactor

import (
	"errors"

	"github.com/gorenx/goren/tools/tool"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func (owner *Grain) initialize(gc *goakt.GrainContext, input Initialize) {
	if input.CreationID == "" ||
		input.AgentID == "" ||
		input.Authority == nil {
		gc.Response(Completion{
			Err: errors.New("tools: tool, Agent and lifecycle identities are required"),
		})
		return
	}
	if owner.business != nil {
		var err error
		if owner.creationID != input.CreationID ||
			owner.agentID != input.AgentID ||
			owner.business.Name() != input.Name ||
			owner.authority.String() != input.Authority.String() {
			err = errors.New("tools: Grain is bound to a different tool identity")
		}
		gc.Response(Completion{
			Err: err,
		})
		return
	}
	constructors, valid := gc.ActorSystem().Extension(providersID).(*Providers)
	if !valid {
		gc.Response(Completion{
			Err: errors.New("tools: tool providers are not installed"),
		})
		return
	}
	source, err := constructors.resolve(input.Name)
	if err != nil {
		gc.Response(Completion{
			Err: err,
		})
		return
	}
	if owner.active != nil {
		gc.Response(Completion{
			Err: errors.New("tools: initialization is still running"),
		})
		return
	}
	if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))); err != nil {
		gc.Response(Completion{
			Err: err,
		})
		return
	}
	current := owner.deferOperation(gc)
	if current == nil {
		return
	}
	gc.RequestGrain(
		input.Authority,
		Claim{
			AgentID:    input.AgentID,
			Name:       input.Name,
			CreationID: input.CreationID,
			InstanceID: owner.instanceID,
		},
	).Then(
		func(payload any, failure error) {
			if owner.active != current {
				return
			}
			if failure == nil {
				claimed, valid := payload.(Completion)
				if !valid {
					failure = errors.New("tools: invalid lifecycle authorization reply")
				} else {
					failure = claimed.Err
				}
			}
			if failure != nil {
				owner.finish(current, failure)
				return
			}
			business, err := tool.New(input.Name)
			if err == nil {
				owner.business = business
				owner.provider = source
				owner.creationID = input.CreationID
				owner.agentID = input.AgentID
				owner.authority = input.Authority
			}
			owner.finish(current, err)
		},
	)
}
