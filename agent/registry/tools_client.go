package registry

import (
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// toolsClient adapts the Registry component protocol to the Tools Supervisor.
// It retains the initialization command and the activation admitted by ClaimTools.
type toolsClient struct {
	command    runtimeactor.InitializeRuntime
	supervisor *runtimeactor.Supervisor
	instanceID string
}

func (endpoint *toolsClient) Runtime() *goakt.GrainIdentity {
	return endpoint.supervisor.Runtime()
}

func (endpoint *toolsClient) Initialize(gc *goakt.GrainContext, reply chan<- componentResult) {
	sender, err := runtimeactor.NewSupervisor(gc.Context(), gc.ActorSystem(), endpoint.command)
	endpoint.supervisor = sender
	if err != nil {
		reply <- componentResult{
			err: err,
		}
		return
	}
	completed := sender.Start(gc)
	err = gc.PipeToSelf(func() (any, error) {
		return clientReply[struct{}]{
			err: <-completed,
			receive: func(_ *goakt.GrainContext, _ struct{}, failure error) {
				reply <- componentResult{
					err: failure,
				}
			},
		}, nil
	})
	if err != nil {
		reply <- componentResult{
			err: err,
		}
	}
}

func (endpoint *toolsClient) Close(gc *goakt.GrainContext, reply chan<- error) {
	completed := endpoint.supervisor.Close(gc, endpoint.instanceID)
	err := gc.PipeToSelf(func() (any, error) {
		return clientReply[struct{}]{
			err: <-completed,
			receive: func(_ *goakt.GrainContext, _ struct{}, failure error) {
				reply <- failure
			},
		}, nil
	})
	if err != nil {
		reply <- err
	}
}
