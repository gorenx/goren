package agent

import "sync"

type registryAdmission uint8

const (
	// registryInactive has no bound Factory and admits no construction.
	registryInactive registryAdmission = iota
	// registryAccepting permits Create and Resume admission for one activation.
	registryAccepting
	// registryDraining joins all Agent lifetimes and rejects new construction.
	registryDraining
)

type lifecycleSignal struct {
	once sync.Once
	done chan struct{}
}

func newLifecycleSignal() lifecycleSignal {
	return lifecycleSignal{
		done: make(chan struct{}),
	}
}

func (signal *lifecycleSignal) close() {
	signal.once.Do(func() {
		close(signal.done)
	})
}

func (signal *lifecycleSignal) Done() <-chan struct{} {
	return signal.done
}
