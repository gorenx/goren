package promptactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
	toolactor "github.com/gorenx/goren/tools/registry/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// RefreshTools requests an authoritative tool baseline for recovery or catch-up.
type RefreshTools struct{}

type toolsBaseline struct {
	definitions tools.Definitions
	err         error
}

func (owner *ToolsSubscriber) refreshTools(rc *goakt.ReceiveContext) {
	request := rc.RequestGrain(owner.registry, toolactor.ReadDefinitions{
		AgentID: owner.agentID,
	})
	if request == nil {
		rc.Tell(owner.coordinator, ToolsApplied{
			AgentID: owner.agentID,
			Err:     ErrUnavailable,
		})
		return
	}
	ctx := context.WithoutCancel(rc.Context())
	self := rc.Self()
	logger := rc.Logger()
	identifier := owner.agentID
	request.Then(func(payload any, err error) {
		result := toolsBaseline{
			err: err,
		}
		if err == nil {
			catalog, ok := payload.(toolactor.Catalog)
			switch {
			case !ok:
				result.err = errors.New("systemprompt: invalid tool baseline response")
			case catalog.Err != nil:
				result.err = catalog.Err
			case !catalog.Found:
				result.err = systemprompt.ErrToolsNotReady
			case catalog.Definitions.AgentID != identifier:
				result.err = errors.New("systemprompt: invalid tool baseline identity or version")
			default:
				result.definitions = catalog.Definitions
			}
		}
		if err := self.Tell(ctx, self, result); err != nil {
			logger.Error("systemprompt: failed to deliver tool baseline to subscriber")
		}
	})
}
