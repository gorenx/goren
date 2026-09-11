package llmactor

import (
	"errors"

	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// initializeAgent obtains Session facts at the configuration owner's boundary.
// Catalog owns restoration and preserves any already-retained configuration.
func (owner *Grain) initializeAgent(gc *goakt.GrainContext, input InitializeAgent) {
	if owner.catalog == nil {
		gc.Response(Completion{Err: ErrModelsUnavailable})
		return
	}
	if input.AgentID == "" || input.Session == nil || input.CreationID == "" {
		gc.Response(Completion{Err: errors.New("llm: Agent initialization requires Session identity")})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(Completion{Err: errors.New("llm: Agent initialization requires a request")})
		return
	}
	gc.RequestGrain(input.Session, sessionactor.Read{CreationID: input.CreationID}, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
		if failure == nil {
			snapshot, valid := payload.(sessionactor.View)
			if !valid {
				failure = errors.New("llm: invalid Session configuration history reply")
			} else if snapshot.Err != nil {
				failure = snapshot.Err
			} else {
				failure = owner.catalog.Restore(&owner.settings, input.AgentID, snapshot.Snapshot.Events)
			}
		}
		reply.Response(Completion{Err: failure})
	})
}
