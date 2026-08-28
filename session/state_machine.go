package session

import (
	"errors"
	"fmt"
)

type lifecycleState uint8

const (
	lifecycleDetachedOpen lifecycleState = iota
	lifecycleEnteredOpen
	lifecycleAnnouncing
	lifecycleLiveOpen
	lifecycleReleasing
	lifecycleSealed
	lifecycleClosed
)

// lifecycleMachine is the sole owner of one Session lifecycle state. It is
// deliberately lock-free: coordinator serializes every command with its queue
// mutex and executes returned decisions after releasing that mutex.
type lifecycleMachine struct {
	state             lifecycleState
	terminalRequested bool
	enterPending      bool
	announcePending   bool
	createdEdge       bool
}

func newLifecycleMachine() lifecycleMachine {
	return lifecycleMachine{state: lifecycleDetachedOpen}
}

func (machine *lifecycleMachine) admit(kind requestKind) error {
	if machine.terminalRequested {
		return ErrWritesClosed
	}
	switch kind {
	case requestCommit:
		if machine.state == lifecycleDetachedOpen ||
			machine.state == lifecycleEnteredOpen ||
			machine.state == lifecycleAnnouncing ||
			machine.state == lifecycleLiveOpen {
			return nil
		}
		return ErrWritesClosed
	case requestEnter:
		if machine.state != lifecycleDetachedOpen || machine.enterPending {
			return errors.New("session: Session is already attached to a Store")
		}
		machine.enterPending = true
		return nil
	case requestAnnounce:
		if machine.state != lifecycleEnteredOpen || machine.announcePending {
			return errors.New("session: Session cannot be announced in its current state")
		}
		machine.announcePending = true
		return nil
	case requestFlush:
		if machine.state == lifecycleEnteredOpen ||
			machine.state == lifecycleAnnouncing ||
			machine.state == lifecycleLiveOpen {
			return nil
		}
		return ErrWritesClosed
	default:
		return errors.New("session: invalid lifecycle admission command")
	}
}

func (machine *lifecycleMachine) finishEnter(succeeded bool) error {
	if !machine.enterPending || machine.state != lifecycleDetachedOpen {
		return errors.New("session: invalid Enter completion")
	}
	machine.enterPending = false
	if succeeded {
		machine.state = lifecycleEnteredOpen
	}
	return nil
}

func (machine *lifecycleMachine) startAnnounce() error {
	if !machine.announcePending || machine.state != lifecycleEnteredOpen {
		return errors.New("session: invalid Announce start")
	}
	machine.announcePending = false
	machine.state = lifecycleAnnouncing
	return nil
}

func (machine *lifecycleMachine) finishAnnounce() error {
	if machine.state != lifecycleAnnouncing {
		return errors.New("session: invalid Announce completion")
	}
	machine.createdEdge = true
	machine.state = lifecycleLiveOpen
	return nil
}

func (machine *lifecycleMachine) requestRelease() (bool, error) {
	if machine.state == lifecycleClosed {
		return false, nil
	}
	if machine.terminalRequested {
		return false, errors.New("session: terminal Release already requested")
	}
	switch machine.state {
	case lifecycleEnteredOpen,
		lifecycleAnnouncing,
		lifecycleLiveOpen,
		lifecycleSealed:
		machine.terminalRequested = true
		return true, nil
	default:
		return false, fmt.Errorf("session: Session cannot be released from lifecycle state %d", machine.state)
	}
}

func (machine *lifecycleMachine) startRelease() error {
	if !machine.terminalRequested {
		return errors.New("session: Release started without terminal admission")
	}
	switch machine.state {
	case lifecycleEnteredOpen, lifecycleLiveOpen, lifecycleSealed:
		machine.state = lifecycleReleasing
		return nil
	default:
		return fmt.Errorf("session: invalid Release start from lifecycle state %d", machine.state)
	}
}

func (machine *lifecycleMachine) finishRelease(succeeded bool) error {
	if machine.state != lifecycleReleasing || !machine.terminalRequested {
		return errors.New("session: invalid Release completion")
	}
	machine.terminalRequested = false
	if succeeded {
		machine.state = lifecycleClosed
	} else {
		machine.state = lifecycleSealed
	}
	return nil
}

func (machine *lifecycleMachine) visible() bool {
	if machine.terminalRequested {
		return false
	}
	return machine.state == lifecycleEnteredOpen ||
		machine.state == lifecycleAnnouncing ||
		machine.state == lifecycleLiveOpen
}

func (machine *lifecycleMachine) publishesEvents() bool {
	return machine.state == lifecycleEnteredOpen ||
		machine.state == lifecycleAnnouncing ||
		machine.state == lifecycleLiveOpen
}

func (machine *lifecycleMachine) isClosed() bool {
	return machine.state == lifecycleClosed
}

func (machine *lifecycleMachine) shouldPublishDisposed() bool {
	return machine.createdEdge
}
