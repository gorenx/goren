package agent

import (
	"errors"

	"github.com/gorenx/goren/session"
)

// ResourceKind identifies an Agent's independently releasable responsibility.
type ResourceKind uint8

const (
	// Work requires confirmation that accepted execution has ended.
	Work ResourceKind = iota
	// Child closes descendants still registered after their managing capabilities stop.
	Child
	// CommandRuntime owns the Agent's command registrations and endpoint.
	CommandRuntime
	// Capability owns an additional component started for this Agent.
	Capability
	// Execution owns model, tool, and maintenance execution resources.
	Execution
	// ToolsRuntime owns pending collection updates and instance cleanup.
	ToolsRuntime
	// PromptRuntime owns the Agent's prompt subscriptions and snapshots.
	PromptRuntime
	// Conversation owns Session registration, flush and shutdown responsibility.
	Conversation
	// AgentLoop retains the AgentLoop Grain until all dependencies have closed.
	AgentLoop
)

// Resource identifies a cleanup responsibility without a runtime address.
// Name is a child ID. Index is the declared capability position.
// Other kinds use neither field. Capabilities close in descending Index order.
type Resource struct {
	Kind  ResourceKind
	Name  string
	Index int
}

// TrackResource records responsibility before an initialization request is sent.
// A closing Agent may still receive facts from an already-admitted startup.
func (directory *Registry) TrackResource(identifier session.SessionID, owned Resource) error {
	entry, found := directory.entries[identifier]
	if !found || entry.lifecycle == Closed {
		return errors.New("agent: resource owner is not registered")
	}
	if owned.Kind > AgentLoop || owned.Kind == Child || owned.Index < 0 ||
		(owned.Kind != Capability && owned.Index != 0) ||
		owned.Name != "" {
		return errors.New("agent: invalid resource responsibility")
	}
	if entry.resources == nil {
		entry.resources = make(map[Resource]struct{})
	}
	entry.resources[owned] = struct{}{}
	directory.entries[identifier] = entry
	return nil
}

// ConfirmRelease accepts the selected cleanup only after its owner confirms it.
// A missing responsibility is already released; retries cannot delete a successor.
func (directory *Registry) ConfirmRelease(identifier session.SessionID, owned Resource) error {
	entry, found := directory.entries[identifier]
	if !found || entry.lifecycle != Closing {
		return errors.New("agent: close has not begun")
	}
	if owned.Kind == Child {
		descendant, found := directory.entries[session.SessionID(owned.Name)]
		if !found || descendant.lifecycle != Closed ||
			descendant.registration.ParentID == nil ||
			*descendant.registration.ParentID != identifier {
			return errors.New("agent: descendant closure is not confirmed")
		}
		return nil
	}
	_, retained := entry.resources[owned]
	if !retained {
		return nil
	}
	expected, pending, err := directory.NextRelease(identifier)
	if err != nil {
		return err
	}
	if !pending || expected != owned {
		return errors.New("agent: resource dependencies have not closed")
	}
	delete(entry.resources, owned)
	return nil
}

// NextRelease selects the next responsibility by dependency order, not a cursor.
// Failed requests do not mutate the ledger. Child membership is its own authority.
func (directory *Registry) NextRelease(identifier session.SessionID) (Resource, bool, error) {
	entry, found := directory.entries[identifier]
	if !found || entry.lifecycle != Closing {
		return Resource{}, false, errors.New("agent: close has not begun")
	}
	var next Resource
	pending := false
	for owned := range entry.resources {
		if !pending || releaseBefore(owned, next) {
			next = owned
			pending = true
		}
	}
	children := directory.Children(identifier)
	if len(children) != 0 {
		descendant := Resource{
			Kind: Child,
			Name: string(children[0]),
		}
		if !pending || releaseBefore(descendant, next) {
			next = descendant
			pending = true
		}
	}
	return next, pending, nil
}

func releaseBefore(left, right Resource) bool {
	if left.Kind != right.Kind {
		return releasePriority[left.Kind] < releasePriority[right.Kind]
	}
	if left.Kind == Capability {
		return left.Index > right.Index
	}
	return left.Name < right.Name
}

// Work settles before capability shutdown. Capabilities close the children they
// manage; Registry then releases remaining children before parent infrastructure.
// Priorities declare release dependencies independently of enum values.
var releasePriority = map[ResourceKind]int{
	Work:           0,
	Capability:     1,
	Child:          2,
	CommandRuntime: 3,
	Execution:      4,
	ToolsRuntime:   5,
	PromptRuntime:  7,
	Conversation:   8,
	AgentLoop:      9,
}
