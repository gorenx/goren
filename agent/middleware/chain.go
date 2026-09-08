// Package middleware owns ordered middleware calls independently of transport.
package middleware

import "errors"

// RegistrationID identifies a definition, never an Agent or persisted event.
type RegistrationID string

// ErrContinued rejects a second downstream request within one invocation.
var ErrContinued = errors.New("agent middleware: downstream was already requested")

type phase uint8

const (
	uninvoked phase = iota
	executing
	waitingDownstream
	downstreamReturned
	completed
)

type frame struct {
	identity RegistrationID
	phase    phase
}

// chain owns ordering only; each extension owns its input and result semantics.
type chain struct {
	frames []frame
	// positions maps each selected definition to its position in this fixed call.
	positions map[RegistrationID]int
	started   bool
	cause     error
}

type delivery struct {
	handler RegistrationID
	invoke  bool
}

func newChain(order []RegistrationID) (chain, error) {
	result := chain{
		frames:    make([]frame, len(order)),
		positions: make(map[RegistrationID]int, len(order)),
	}
	for index, identity := range order {
		if identity == "" {
			return chain{}, errors.New("agent middleware: empty registration")
		}
		if _, found := result.positions[identity]; found {
			return chain{}, errors.New("agent middleware: duplicate registration")
		}
		result.positions[identity] = index
		result.frames[index].identity = identity
	}
	return result, nil
}

func (call *chain) start() (delivery, error) {
	if call.started {
		return delivery{}, errors.New("agent middleware: call already started")
	}
	call.started = true
	if call.cause != nil || len(call.frames) == 0 {
		return delivery{}, nil
	}
	call.frames[0].phase = executing
	return delivery{
		handler: call.frames[0].identity,
		invoke:  true,
	}, nil
}

func (call *chain) find(identity RegistrationID) (int, error) {
	if !call.started {
		return 0, errors.New("agent middleware: call has not started")
	}
	index, found := call.positions[identity]
	if !found {
		return 0, errors.New("agent middleware: unknown registration")
	}
	return index, nil
}

func (call *chain) proceed(identity RegistrationID) (delivery, error) {
	if call.cause != nil {
		return delivery{}, call.cause
	}
	index, err := call.find(identity)
	if err != nil {
		return delivery{}, err
	}
	current := &call.frames[index]
	switch current.phase {
	case waitingDownstream, downstreamReturned:
		return delivery{}, ErrContinued
	case executing:
	default:
		return delivery{}, errors.New("agent middleware: invocation is not open")
	}
	if index+1 == len(call.frames) {
		current.phase = downstreamReturned
		return delivery{
			handler: identity,
		}, nil
	}
	current.phase = waitingDownstream
	call.frames[index+1].phase = executing
	return delivery{
		handler: call.frames[index+1].identity,
		invoke:  true,
	}, nil
}

func (call *chain) finish(identity RegistrationID) (RegistrationID, error) {
	index, err := call.find(identity)
	if err != nil {
		return "", err
	}
	current := &call.frames[index]
	if current.phase != executing && current.phase != downstreamReturned {
		return "", errors.New("agent middleware: invocation cannot complete before its downstream")
	}
	var parent RegistrationID
	if index > 0 {
		if call.frames[index-1].phase != waitingDownstream {
			return "", errors.New("agent middleware: parent is not waiting")
		}
		parent = call.frames[index-1].identity
		call.frames[index-1].phase = downstreamReturned
	}
	current.phase = completed
	return parent, nil
}
