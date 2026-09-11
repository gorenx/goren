// Package modelconfig owns versioned model configuration, separate from model I/O.
package modelconfig

import (
	"context"
	"errors"
	"github.com/gorenx/goren/llm"
)

// ErrVersion identifies a request against a configuration that is no longer current.
var ErrVersion = errors.New("modelconfig: version conflict")

// Provider declares a routing name and a credential reference, never a secret.
type Provider struct {
	ID            string
	CredentialRef string
}

// Definition contains deployment defaults and available provider configuration.
type Definition struct {
	Default   llm.CallConfig
	Providers []Provider
}

// Snapshot is detached from future configuration changes.
type Snapshot struct {
	Version    uint64
	Definition Definition
}

// Resolved contains request parameters and their configuration provenance.
type Resolved struct {
	Version       uint64
	Config        llm.CallConfig
	CredentialRef string
}

// Reader is the configuration boundary used by non-actor consumers.
type Reader interface {
	CurrentConfig(context.Context) (Snapshot, error)
}
