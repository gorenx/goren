package credentials

import (
	"context"
	"errors"
	"fmt"
)

// Manager owns credential precedence and mutation semantics independently of storage.
type Manager struct {
	store       LiveStore
	environment Environment
}

// NewManager constructs the Credentials capability over one credential store.
func NewManager(storage LiveStore, platform Environment) (*Manager, error) {
	if storage == nil {
		return nil, errors.New("credentials: store is required")
	}
	if platform == nil {
		return nil, errors.New("credentials: environment is required")
	}
	return &Manager{
		store:       storage,
		environment: platform,
	}, nil
}

// Resolve applies launching-environment precedence before managed storage.
func (owner *Manager) Resolve(requestContext context.Context, credentialRef string) (Resolved, error) {
	if err := requestContext.Err(); err != nil {
		return Resolved{}, err
	}
	if err := ValidateRef(credentialRef); err != nil {
		return Resolved{}, err
	}
	if value, found := owner.inherited(credentialRef); found {
		return Resolved{
			Value:  value,
			Source: "env",
		}, nil
	}
	value, found, err := owner.store.Load(requestContext, credentialRef)
	if err != nil {
		return Resolved{}, err
	}
	if !found || value == "" {
		return Resolved{}, ErrNotFound
	}
	return Resolved{
		Value:  value,
		Source: owner.store.Source(),
	}, nil
}

// Describe reports safe configuration facts without resolving a value to the caller.
func (owner *Manager) Describe(requestContext context.Context, credentialRef string) (Info, error) {
	if err := requestContext.Err(); err != nil {
		return Info{}, err
	}
	if err := ValidateRef(credentialRef); err != nil {
		return Info{}, err
	}
	if _, found := owner.inherited(credentialRef); found {
		return Info{
			Configured: true,
			Source:     "env",
			Writable:   false,
		}, nil
	}
	_, configured, err := owner.store.Load(requestContext, credentialRef)
	if err != nil {
		return Info{}, err
	}
	source := ""
	if configured {
		source = owner.store.Source()
	}
	return Info{
		Configured: configured,
		Source:     source,
		Writable:   true,
	}, nil
}

// Set validates and commits one managed credential unless the environment shadows it.
func (owner *Manager) Set(requestContext context.Context, credentialRef string, value string) error {
	if err := ValidateRef(credentialRef); err != nil {
		return err
	}
	if value == "" {
		return errors.New("credentials: empty values cannot be stored; use unset")
	}
	if _, found := owner.inherited(credentialRef); found {
		return fmt.Errorf("credentials: %q is supplied read-only by the launching environment", credentialRef)
	}
	return owner.store.Save(requestContext, credentialRef, value)
}

// Unset removes one managed value unless the environment shadows it.
func (owner *Manager) Unset(requestContext context.Context, credentialRef string) error {
	if err := ValidateRef(credentialRef); err != nil {
		return err
	}
	if _, found := owner.inherited(credentialRef); found {
		return fmt.Errorf("credentials: %q is supplied read-only by the launching environment", credentialRef)
	}
	return owner.store.Delete(requestContext, credentialRef)
}

func (owner *Manager) inherited(credentialRef string) (string, bool) {
	value, found := owner.environment.Lookup(credentialRef)
	return value, found && value != ""
}
