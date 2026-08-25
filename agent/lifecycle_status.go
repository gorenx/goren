package agent

import "sync"

type epochPhase uint8

const (
	// epochMaterializing reserves the exact Session ID and optional runtime
	// parent while the Factory has not attached an Agent runtime yet.
	epochMaterializing epochPhase = iota
	// epochAttached has one exact Agent and runtime attached but has not
	// committed Agent publication or opened normal work admission.
	epochAttached
	// epochLive is a committed Agent epoch visible through Registry queries.
	epochLive
	// epochClosing rejects new descendants and is owned by one teardown
	// transaction that must reach epochClosed.
	epochClosing
	// epochClosed is terminal; the epoch is absent from Registry indexes and
	// retains only its stable close result for exact Handle calls.
	epochClosed
)

type publicationPhase uint8

const (
	// publicationUnpublished means no Agent Created dispatch has begun.
	publicationUnpublished publicationPhase = iota
	// publicationPublishing permits listener-driven descendant construction
	// while the outer Agent Created dispatch is still deciding commit.
	publicationPublishing
	// publicationPublished means Created dispatch began and rollback must emit
	// the paired Agent Disposed fact, even if a listener returned an error.
	publicationPublished
	// publicationRetired means the paired Disposed fact has been requested and
	// must not be published again.
	publicationRetired
)

type descendantAdmission uint8

const (
	// descendantsAccepted allows new child epochs under this exact parent.
	descendantsAccepted descendantAdmission = iota
	// descendantsDraining permanently rejects new child epochs while
	// admitted and live descendants are joined child-first.
	descendantsDraining
)

type teardownOrigin uint8

const (
	// teardownUnclaimed means neither the Coordinator nor structural Runtime
	// teardown has claimed the epoch close transaction.
	teardownUnclaimed teardownOrigin = iota
	// teardownByCoordinator means an exact Handle, parent close, or Registry
	// shutdown owns descendant drain and invokes the runtime teardown port.
	teardownByCoordinator
	// teardownByRuntime means Plugin Runtime structural disposal began first and
	// AgentTeardown callbacks report completion to the same Coordinator epoch.
	teardownByRuntime
)

type registryAdmission uint8

const (
	// registryAccepting permits Factory registration and atomic Create/Resume
	// epoch creation.
	registryAccepting registryAdmission = iota
	// registryDraining has one RegistryService.Shutdown owner joining every
	// exact root and its descendants. Concurrent callers wait for shutdown.
	registryDraining
	// registryClosed is terminal and retains the stable Shutdown result.
	registryClosed
)

type factoryRegistrationState uint8

const (
	// factoryRegistered accepts new construction through this exact Factory
	// while the Registry itself remains accepting.
	factoryRegistered factoryRegistrationState = iota
	// factoryRegistrationClosing has removed this exact Factory from routing
	// and is waiting for every construction it already admitted to finish.
	factoryRegistrationClosing
	// factoryRegistrationClosed is terminal; its close completion signal and
	// construction set are both settled.
	factoryRegistrationClosed
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
