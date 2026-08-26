package continuation

import (
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

type activationAdmission uint8

const (
	// activationsAccepted permits new or resumed continuable Activation
	// publication after the exact runtime parent passes Registry admission.
	activationsAccepted activationAdmission = iota
	// activationsClosing permanently rejects new business Activations while
	// resident Activations enter their managed close operations.
	activationsClosing
)

// activationRegistry owns the process-local continuable Activation index,
// per-child serialization locks, and its single admission state. Runtime
// Agent parent-child ownership remains in agent.LifecycleCoordinator.
type activationRegistry struct {
	mutex       sync.Mutex
	activations map[session.SessionID]*Activation
	locks       map[session.SessionID]*sync.Mutex
	admission   activationAdmission
}

func newActivationRegistry() *activationRegistry {
	return &activationRegistry{
		activations: make(map[session.SessionID]*Activation),
		locks:       make(map[session.SessionID]*sync.Mutex),
	}
}

// disposal is one memoized Activation release transaction. Its presence is
// the admission cutoff; Agent Registry owns descendant ordering while this
// transaction owns continuation settlement and durable flush semantics.
type disposal struct {
	done chan struct{}
	err  error
}

// Activation is one resident epoch of a durable continuable child.
type Activation struct {
	childID      session.SessionID
	parentID     session.SessionID
	parent       agent.Agent
	providerName string
	handle       agent.Handle
	accepted     map[llm.MessageID]struct{}
	wake         chan struct{}
	runID        subagent.RunID
	boundary     int64
	announced    bool
	disposal     *disposal
}
