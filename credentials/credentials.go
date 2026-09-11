// Package credentials owns secret references and the provider-facing credential capability.
package credentials

import (
	"context"
	"errors"
	"regexp"
)

var referencePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ErrNotFound means no non-empty credential exists in any source.
var ErrNotFound = errors.New("credentials: credential not found")

// ValidateRef checks the POSIX environment-variable name used as a credential reference.
func ValidateRef(value string) error {
	if !referencePattern.MatchString(value) {
		return errors.New("credential reference must be a POSIX environment-variable name")
	}
	return nil
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
	Resolve(context.Context, string) (Resolved, error)
	Describe(context.Context, string) (Info, error)
	Set(context.Context, string, string) error
	Unset(context.Context, string) error
}

// LiveStore is the storage-only port consumed by Manager. It assigns no
// precedence and never consults the process environment.
type LiveStore interface {
	Load(context.Context, string) (string, bool, error)
	Save(context.Context, string, string) error
	Delete(context.Context, string) error
	Source() string
}

// Environment supplies the read-only launch layer outside typed config.
type Environment interface {
	Lookup(string) (string, bool)
}
