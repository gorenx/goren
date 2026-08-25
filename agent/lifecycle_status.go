package agent

import "sync"

type epochPhase uint8

const (
	epochMaterializing epochPhase = iota
	epochAttached
	epochLive
	epochClosing
	epochClosed
)

type publicationPhase uint8

const (
	publicationUnpublished publicationPhase = iota
	publicationPublishing
	publicationPublished
	publicationRetired
)

type descendantAdmission uint8

const (
	descendantsAccepted descendantAdmission = iota
	descendantsDraining
)

type teardownOrigin uint8

const (
	teardownUnclaimed teardownOrigin = iota
	teardownByCoordinator
	teardownByRuntime
)

type registryAdmission uint8

const (
	registryAccepting registryAdmission = iota
	registryShuttingDown
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
