package execution

import (
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

// Entry is the routing identity for one published live Execution.
type Entry struct {
	Execution *Execution
	Mode      subagent.Mode
	Parent    agent.Agent
	Subject   agent.Agent
	Closing   <-chan struct{}
}

// Registry is the one process-local index shared by both Subagent
// implementations. Agent Registry remains the owner of exact Agent epochs.
type Registry struct {
	mutex   sync.RWMutex
	entries map[session.SessionID]Entry
}

// NewRegistry constructs an empty live Execution index.
func NewRegistry() *Registry {
	return &Registry{
		entries: make(map[session.SessionID]Entry),
	}
}

// Publish installs one active exact Execution.
func (registryOwner *Registry) Publish(candidate Entry) error {
	if candidate.Execution == nil || candidate.Parent == nil ||
		candidate.Subject == nil || candidate.Closing == nil {
		return errors.New("subagent: published Execution entry is incomplete")
	}
	if candidate.Execution.State() != subagent.ExecutionActive {
		return errors.New("subagent: only an active Execution can be published")
	}
	targetID := candidate.Execution.ChildID()
	if targetID != candidate.Subject.ID() {
		return errors.New("subagent: Execution and Agent identities differ")
	}
	registryOwner.mutex.Lock()
	defer registryOwner.mutex.Unlock()
	if _, exists := registryOwner.entries[targetID]; exists {
		return &subagent.Error{
			Code:    subagent.ErrorDuplicateChild,
			Message: "a live Subagent Execution already uses this child ID",
		}
	}
	registryOwner.entries[targetID] = candidate
	return nil
}

// Find returns the published Entry for a child ID.
func (registryOwner *Registry) Find(targetID session.SessionID) (Entry, bool) {
	registryOwner.mutex.RLock()
	record, found := registryOwner.entries[targetID]
	registryOwner.mutex.RUnlock()
	return record, found
}

// Remove deletes only the exact matching Execution.
func (registryOwner *Registry) Remove(running *Execution) {
	if running == nil {
		return
	}
	registryOwner.mutex.Lock()
	record, found := registryOwner.entries[running.ChildID()]
	if found && record.Execution == running {
		delete(registryOwner.entries, running.ChildID())
	}
	registryOwner.mutex.Unlock()
}

// List returns a detached snapshot of current entries. Callers must not infer
// ordering from the process-local index.
func (registryOwner *Registry) List() []Entry {
	registryOwner.mutex.RLock()
	entries := make([]Entry, 0, len(registryOwner.entries))
	for _, record := range registryOwner.entries {
		entries = append(entries, record)
	}
	registryOwner.mutex.RUnlock()
	return entries
}
