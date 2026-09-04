package agentloop

import (
	"errors"
	"sync"
)

// pluginActivation owns the reversible resources of one Plugin activation.
type pluginActivation struct {
	gateway     *pluginGateway
	effectCalls sync.WaitGroup
	driverDone  chan struct{}
}

func newPluginActivation() *pluginActivation {
	return &pluginActivation{
		gateway:    newPluginGateway(),
		driverDone: make(chan struct{}),
	}
}

func (activation *pluginActivation) eventGateway() agentEvents {
	return activation.gateway
}

func (activation *pluginActivation) waterfallGateway() agentWaterfalls {
	return activation.gateway
}

func (activation *pluginActivation) start(owner *Plugin) {
	go activation.drive(owner)
}

func (activation *pluginActivation) drive(owner *Plugin) {
	defer close(activation.driverDone)
	for {
		select {
		case request := <-activation.gateway.requests:
			activation.effectCalls.Add(1)
			go func() {
				defer activation.effectCalls.Done()
				owner.executeEffect(request)
			}()
		case <-activation.gateway.stopped:
			return
		}
	}
}

func (activation *pluginActivation) close() {
	activation.gateway.stop(errors.New("agentloop: Plugin is stopping"))
	<-activation.driverDone
	activation.effectCalls.Wait()
}
