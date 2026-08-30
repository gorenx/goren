package agent

import "sync"

type recordPhase uint8

const (
	// recordConstructing reserves a Session ID while Factory builds the Host.
	recordConstructing recordPhase = iota
	// recordAttached owns a Host that has not completed Agent publication.
	recordAttached
	// recordLive is visible and accepts Scope operations and descendants.
	recordLive
	// recordClosing rejects work while child-first teardown completes.
	recordClosing
	// recordClosed is terminal and absent from Registry indexes.
	recordClosed
)

type publicationPhase uint8

const (
	// publicationUnpublished means agent/created dispatch has not begun.
	publicationUnpublished publicationPhase = iota
	// publicationPublishing permits descendants created by Created observers.
	publicationPublishing
	// publicationPublished requires one paired agent/disposed dispatch.
	publicationPublished
	// publicationRetired means agent/disposed dispatch was already attempted.
	publicationRetired
)

type descendantAdmission uint8

const (
	// descendantsAccepted permits child construction under this exact Agent.
	descendantsAccepted descendantAdmission = iota
	// descendantsClosing permanently rejects new children.
	descendantsClosing
)

type registryAdmission uint8

const (
	// registryAccepting permits Create and Resume admission.
	registryAccepting registryAdmission = iota
	// registryDraining joins all records and rejects new construction.
	registryDraining
	// registryClosed is terminal and retains the stable shutdown result.
	registryClosed
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
