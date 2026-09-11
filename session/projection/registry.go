package projection

import (
	"errors"
	"fmt"
	"github.com/google/uuid"
	"strings"
)

type registration struct {
	projectionUnit Unit
	refs           int
}

// Registry owns Unit definitions and registration lifetimes. Its owner serializes calls.
type Registry struct {
	// registrationsByID maps one registration token to the Unit key it retains.
	// Remove consumes this token once, so an old token cannot release a later Unit.
	registrationsByID map[RegistrationID]string
	// registrations maps Unit.Key() to its active definition and reference count.
	// Compatible registration tokens share the same definition object.
	registrations map[string]*registration
	order         []string
}

// NewRegistry constructs an empty projection registry.
func NewRegistry() *Registry {
	return &Registry{
		registrations:     make(map[string]*registration),
		registrationsByID: make(map[RegistrationID]string),
	}
}

// Register installs or reference-counts one domain projection definition.
func (owner *Registry) Register(projectionUnit Unit) (RegistrationID, error) {
	if projectionUnit == nil {
		return "", errors.New("sessionprojection: projection Unit is nil")
	}
	projectionKey := projectionUnit.Key()
	trimmedKey := strings.TrimSpace(projectionKey)
	if trimmedKey == "" || projectionKey != trimmedKey {
		return "", errors.New("sessionprojection: projection key must be non-empty and trimmed")
	}
	version := projectionUnit.StateVersion()
	if version < 0 {
		return "", fmt.Errorf("sessionprojection: projection %q stateVersion must be a non-negative integer", projectionKey)
	}

	existing := owner.registrations[projectionKey]
	if existing == nil {
		owner.registrations[projectionKey] = &registration{
			projectionUnit: projectionUnit,
			refs:           1,
		}
		owner.order = append(owner.order, projectionKey)
	} else if existing.projectionUnit.StateVersion() != version {
		return "", fmt.Errorf(
			"sessionprojection: key %q is already registered at stateVersion %d; refusing stateVersion %d",
			projectionKey, existing.projectionUnit.StateVersion(), version,
		)
	} else {
		existing.refs++
	}

	id := RegistrationID(uuid.NewString())
	owner.registrationsByID[id] = projectionKey
	return id, nil
}

// Remove releases one registration. Repeating an old ID has no effect.
func (owner *Registry) Remove(id RegistrationID) {
	key, found := owner.registrationsByID[id]
	if !found {
		return
	}
	delete(owner.registrationsByID, id)
	owner.unregister(key)
}

func (owner *Registry) unregister(projectionKey string) {
	entry := owner.registrations[projectionKey]
	if entry == nil {
		return
	}
	entry.refs--
	if entry.refs != 0 {
		return
	}
	delete(owner.registrations, projectionKey)
	for index, candidate := range owner.order {
		if candidate == projectionKey {
			owner.order = append(owner.order[:index], owner.order[index+1:]...)
			break
		}
	}
}

// NewProjection creates an independent fold over the currently registered Units.
func (owner *Registry) NewProjection() *Projection {
	return &Projection{
		registry: owner,
		cells:    make(map[*registration]unitCell),
	}
}
