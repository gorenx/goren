package continuation

import (
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

// residency owns the process-local continuation forest, per-child admission
// locks, and scoped/global teardown cutoffs. Manager coordinates use cases
// against this owner instead of owning each mutable collection independently.
type residency struct {
	mutex        sync.Mutex
	activations  map[session.SessionID]*Activation
	locks        map[session.SessionID]*sync.Mutex
	closingRoots map[session.SessionID]agent.Agent
	building     map[*materialization]struct{}
	draining     bool
}

func newResidency() *residency {
	return &residency{
		activations:  make(map[session.SessionID]*Activation),
		locks:        make(map[session.SessionID]*sync.Mutex),
		closingRoots: make(map[session.SessionID]agent.Agent),
		building:     make(map[*materialization]struct{}),
	}
}

// materialization is one admitted but not yet resident child transaction.
type materialization struct {
	lineage []agent.Agent
	done    chan struct{}
}

// Activation is one resident epoch of a durable continuable child.
type Activation struct {
	childID       session.SessionID
	parentID      session.SessionID
	providerName  string
	handle        agent.Handle
	ancestry      []agent.Agent
	ownedChildren map[session.SessionID]struct{}
	accepted      map[llm.MessageID]struct{}
	wake          chan struct{}
	runID         subagent.RunID
	announced     bool
	closing       bool
	disposeDone   chan struct{}
	disposeErr    error
}
