// Package credentials owns secret references and the provider-facing credential capability.
package credentials

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"

	"github.com/gorenx/goren/plugin"
)

var referencePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Ref identifies one credential without carrying its value.
type Ref string

// NewRef validates the POSIX environment-variable name used as a credential reference.
func NewRef(value string) (Ref, error) {
	if !referencePattern.MatchString(value) {
		return "", errors.New("credential reference must be a POSIX environment-variable name")
	}
	return Ref(value), nil
}

// Resolved contains a secret and the provider-defined source that supplied it.
// It must remain inside the operation that requested it.
type Resolved struct {
	Value  string
	Source string
}

// Info is safe for configuration UIs because it never contains the secret value.
type Info struct {
	Configured bool
	Source     string
	Writable   bool
}

// Provider resolves and mutates credential references. Consumers resolve once
// per operation so a committed rotation affects the next operation immediately.
type Provider interface {
	Resolve(context.Context, Ref) (Resolved, bool, error)
	Describe(context.Context, Ref) (Info, error)
	Set(context.Context, Ref, string) error
	Unset(context.Context, Ref) error
}

// Store is the storage-only port consumed by Manager. It assigns no
// precedence and never consults the process environment.
type Store interface {
	Load(context.Context, Ref) (string, bool, error)
	Save(context.Context, Ref, string) error
	Delete(context.Context, Ref) error
	Source() string
}

// Environment supplies the read-only launch layer outside typed config.
type Environment struct {
	LookupEnv func(string) (string, bool)
}

// Manager owns credential precedence and mutation semantics independently of storage.
type Manager struct {
	store     Store
	lookupEnv func(string) (string, bool)
}

// NewManager constructs the Credentials capability over one credential store.
func NewManager(storage Store, platform Environment) (*Manager, error) {
	if storage == nil {
		return nil, errors.New("credentials: store is required")
	}
	lookup := platform.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	return &Manager{store: storage, lookupEnv: lookup}, nil
}

// Resolve applies launching-environment precedence before managed storage.
func (owner *Manager) Resolve(requestContext context.Context, credentialRef Ref) (Resolved, bool, error) {
	if err := requestContext.Err(); err != nil {
		return Resolved{}, false, err
	}
	if value, found := owner.inherited(credentialRef); found {
		return Resolved{Value: value, Source: "env"}, true, nil
	}
	value, found, err := owner.store.Load(requestContext, credentialRef)
	if err != nil || !found || value == "" {
		return Resolved{}, false, err
	}
	return Resolved{Value: value, Source: owner.store.Source()}, true, nil
}

// Describe reports safe configuration facts without resolving a value to the caller.
func (owner *Manager) Describe(requestContext context.Context, credentialRef Ref) (Info, error) {
	if err := requestContext.Err(); err != nil {
		return Info{}, err
	}
	if _, found := owner.inherited(credentialRef); found {
		return Info{Configured: true, Source: "env", Writable: false}, nil
	}
	_, configured, err := owner.store.Load(requestContext, credentialRef)
	if err != nil {
		return Info{}, err
	}
	source := ""
	if configured {
		source = owner.store.Source()
	}
	return Info{Configured: configured, Source: source, Writable: true}, nil
}

// Set validates and commits one managed credential unless the environment shadows it.
func (owner *Manager) Set(requestContext context.Context, credentialRef Ref, value string) error {
	if value == "" {
		return errors.New("credentials: empty values cannot be stored; use unset")
	}
	if _, found := owner.inherited(credentialRef); found {
		return fmt.Errorf("credentials: %q is supplied read-only by the launching environment", credentialRef)
	}
	return owner.store.Save(requestContext, credentialRef, value)
}

// Unset removes one managed value unless the environment shadows it.
func (owner *Manager) Unset(requestContext context.Context, credentialRef Ref) error {
	if _, found := owner.inherited(credentialRef); found {
		return fmt.Errorf("credentials: %q is supplied read-only by the launching environment", credentialRef)
	}
	return owner.store.Delete(requestContext, credentialRef)
}

func (owner *Manager) inherited(credentialRef Ref) (string, bool) {
	value, found := owner.lookupEnv(string(credentialRef))
	return value, found && value != ""
}

// Service is the canonical plugin capability implemented by credential providers.
var Service = plugin.DefineService[Provider]("credentials")
