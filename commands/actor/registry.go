package commandactor

import (
	"context"
	"errors"
	"reflect"

	"github.com/gorenx/goren/commands"
	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Registry serializes the global catalog, Session handlers and retry identities.
// It never executes a command or owns an Agent's work.
type Registry struct {
	business commands.Catalog
	// handlers maps a configured global or Session command to its publication ID.
	// instances supplies Session capability addresses without overriding metadata.
	handlers     map[route]string
	instances    map[route]string
	publications map[string]publication
	// retired prevents delayed registration messages from undoing withdrawal.
	retired map[string]struct{}
}

type route struct {
	sessionID session.SessionID
	name      string
}
type publication struct {
	input     Register
	inherited bool
	entry     commands.Registration
}

// OnActivate creates the in-memory registry. Production uses LongLivedGrain.
func (owner *Registry) OnActivate(context.Context, *goakt.GrainProps) error {
	owner.business = commands.Catalog{}
	owner.handlers = make(map[route]string)
	owner.instances = make(map[route]string)
	owner.publications = make(map[string]publication)
	owner.retired = make(map[string]struct{})
	return nil
}

// OnDeactivate does not certify execution cleanup. Owners must stop before Registry.
func (owner *Registry) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }

// OnReceive atomically publishes definitions and addresses, or resolves inheritance.
func (owner *Registry) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case Register:
		gc.Response(owner.register(input, false))
	case Advertise:
		gc.Response(owner.register(Register{
			ID:         input.ID,
			SessionID:  input.SessionID,
			Definition: input.Definition,
			Handler:    input.Handler,
		}, true))
	case Unregister:
		entry, found := owner.publications[input.ID]
		if found {
			handlers := owner.handlers
			if entry.inherited {
				handlers = owner.instances
			}
			delete(handlers, route{
				sessionID: entry.input.SessionID,
				name:      entry.input.Definition.Name,
			})
			if !entry.inherited {
				owner.business.Unregister(entry.input.SessionID, entry.entry)
			}
			delete(owner.publications, input.ID)
		}
		if input.ID != "" {
			owner.retired[input.ID] = struct{}{}
		}
		gc.Response(Unregistered{
			Removed: found,
		})
	case List:
		gc.Response(Descriptors{
			Values: owner.business.List(input.SessionID),
		})
	case Resolve:
		entry, found := owner.business.Find(input.SessionID, input.Name)
		id := owner.handlers[route{
			sessionID: input.SessionID,
			name:      input.Name,
		}]
		if id == "" {
			id = owner.instances[route{
				sessionID: input.SessionID,
				name:      input.Name,
			}]
		}
		if id == "" {
			id = owner.handlers[route{
				name: input.Name,
			}]
		}
		endpoint := owner.publications[id].input.Handler
		gc.Response(Resolved{
			Registration: entry,
			Handler:      endpoint,
			Found:        found && endpoint != nil,
		})
	default:
		gc.Unhandled()
	}
}

func (owner *Registry) register(input Register, inherited bool) Registered {
	result := Registered{
		ID: input.ID,
	}
	if input.ID == "" || input.Handler == nil || (inherited && input.SessionID == "") {
		result.Err = errors.New("commands: publication identity, handler and advertisement Session are required")
		return result
	}
	if err := input.Handler.Validate(); err != nil {
		result.Err = err
		return result
	}
	if _, closed := owner.retired[input.ID]; closed {
		result.Err = errors.New("commands: publication was withdrawn")
		return result
	}
	if previous, found := owner.publications[input.ID]; found {
		if previous.inherited != inherited || previous.input.SessionID != input.SessionID || !previous.input.Handler.Equal(input.Handler) || !reflect.DeepEqual(previous.input.Definition, input.Definition) {
			result.Err = errors.New("commands: publication identity conflicts with earlier content")
		} else {
			result.Registration = previous.entry
			result.Registration.Definition = previous.entry.Definition.Clone()
		}
		return result
	}
	key := route{
		sessionID: input.SessionID,
		name:      input.Definition.Name,
	}
	handlers := owner.handlers
	if inherited {
		handlers = owner.instances
	}
	if handlers[key] != "" {
		result.Err = errors.New("commands: route already registered")
		return result
	}
	var entry commands.Registration
	var err error
	if inherited {
		// Validate even when a configured default already exists; advertisers cannot
		// overwrite that default or turn a Session route into a metadata override.
		probe := commands.Registry{}
		if _, err = probe.Register(input.Definition); err == nil {
			var found bool
			entry, found = owner.business.Find("", input.Definition.Name)
			if !found {
				entry, err = owner.business.Register("", input.Definition)
			}
		}
	} else {
		entry, err = owner.business.Register(input.SessionID, input.Definition)
	}
	if err != nil {
		result.Err = err
		return result
	}
	// Retain detached metadata for exact retry comparison, independently of defaults.
	input.Definition = input.Definition.Clone()
	owner.publications[input.ID] = publication{
		input:     input,
		inherited: inherited,
		entry: commands.Registration{
			ID:         entry.ID,
			Definition: entry.Definition.Clone(),
		},
	}
	handlers[key] = input.ID
	result.Registration = entry
	return result
}
