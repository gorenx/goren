package toolactor

import (
	"errors"

	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/tools/tool"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func (owner *Grain) initialize(gc *goakt.GrainContext, input Initialize) {
	if input.CreationID == "" || input.AgentID == "" || input.Registry == nil || input.Authority == nil {
		gc.Response(Completion{
			Err: errors.New("tools: tool, Agent, catalog and lifecycle identities are required"),
		})
		return
	}
	if owner.business != nil {
		var err error
		if owner.creationID != input.CreationID ||
			owner.agentID != input.AgentID ||
			owner.business.Name() != input.Name ||
			owner.registry.String() != input.Registry.String() ||
			owner.authority.String() != input.Authority.String() {
			err = errors.New("tools: Grain is bound to a different tool identity")
		}
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
		agentactor.ClaimTool{
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
				claimed, valid := payload.(agentactor.Claimed)
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
				owner.creationID = input.CreationID
				owner.agentID = input.AgentID
				owner.registry = input.Registry
				owner.authority = input.Authority
			}
			owner.finish(current, err)
		},
	)
}
