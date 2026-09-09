package agent

import (
	"errors"

	"github.com/gorenx/goren/session"
)

// Lifecycle describes Agent availability, independently of framework startup steps.
type Lifecycle uint8

const (
	// Initializing reserves identity while required components become ready.
	Initializing Lifecycle = iota
	// Available permits publication of the initialized Agent to callers.
	Available
	// Closing rejects new work while original resource responsibilities remain.
	Closing
	// Closed confirms lifecycle removal after resource and descendant shutdown.
	Closed
)

type membership struct {
	registration Registration
	lifecycle    Lifecycle
	failure      error
	requirements []requirement
	ready        map[Component]struct{}
	resources    map[Resource]struct{}
}

// State reads the latest lifecycle, including a retained completed close.
func (directory *Registry) State(identifier session.SessionID) (Lifecycle, bool) {
	entry, found := directory.entries[identifier]
	return entry.lifecycle, found
}

// CompleteInitialization publishes availability only while admission is open.
func (directory *Registry) CompleteInitialization(identifier session.SessionID) error {
	err := directory.CheckAdmission(identifier)
	if err != nil {
		return err
	}
	entry := directory.entries[identifier]
	if entry.lifecycle != Initializing && entry.lifecycle != Available {
		return errors.New("agent: initialization cannot complete in the current lifecycle")
	}
	_, pending, err := directory.NextComponent(identifier)
	if err != nil {
		return err
	} else if pending {
		return errors.New("agent: required components have not initialized")
	}

	entry.lifecycle = Available
	directory.entries[identifier] = entry
	return nil
}

// CheckReady requires completed initialization as well as ancestor admission.
func (directory *Registry) CheckReady(identifier session.SessionID) error {
	err := directory.CheckAdmission(identifier)
	if err != nil {
		return err
	}
	if directory.entries[identifier].lifecycle != Available {
		return errors.New("agent: initialization has not completed")
	}
	return nil
}

// FailInitialization preserves the first startup error and requires cleanup.
// Cleanup errors belong to the close attempt and never overwrite this result.
func (directory *Registry) FailInitialization(identifier session.SessionID, failure error) error {
	entry, found := directory.entries[identifier]
	if !found || entry.lifecycle == Closed || entry.lifecycle == Available || failure == nil {
		return errors.New("agent: initialization failure is not valid in the current lifecycle")
	}
	if entry.failure == nil {
		entry.failure = failure
	}
	entry.lifecycle = Closing
	directory.entries[identifier] = entry
	return nil
}

// InitializationError retains the original failure through cleanup and close.
func (directory *Registry) InitializationError(identifier session.SessionID) error {
	return directory.entries[identifier].failure
}

// IsClosed reports confirmed removal, not an absent or still-closing identity.
func (directory *Registry) IsClosed(identifier session.SessionID) bool {
	entry, found := directory.entries[identifier]
	return found && entry.lifecycle == Closed
}
