package promptactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/tools"
	toolactor "github.com/gorenx/goren/tools/runtime/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// RefreshTools requests an authoritative tool baseline for recovery or catch-up.
type RefreshTools struct{}

type toolsBaseline struct {
	definitions tools.Definitions
	err         error
}

func (owner *ToolsSubscriber) refreshTools(rc *goakt.ReceiveContext) {
	if owner.refreshing {
		owner.refreshPending = true
		return
	}
	owner.refreshing = true
	request := rc.RequestGrain(owner.registry, toolactor.ReadDefinitions{
		CreationID: owner.creationID,
	})
	if request == nil {
		owner.finishRefresh(rc, ErrUnavailable)
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
			catalog, ok := payload.(toolactor.Definitions)
			switch {
			case !ok:
				result.err = errors.New("systemprompt: invalid tool baseline response")
			case catalog.Err != nil:
				result.err = catalog.Err
			case catalog.Value.AgentID != identifier:
				result.err = errors.New("systemprompt: invalid tool baseline identity")
			default:
				result.definitions = catalog.Value
			}
		}
		if err := self.Tell(ctx, self, result); err != nil {
			logger.Error("systemprompt: failed to deliver tool baseline to subscriber")
		}
	})
}
