// Package factory owns strict configuration and construction of the Session
// Store Plugin.
package factory

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/session"
)

// Factory constructs the canonical Session Store Plugin.
type Factory struct {
	postCommitFailures session.PostCommitFailureReporter
}

// New constructs a statically linked Session Factory.
func New(postCommitFailures session.PostCommitFailureReporter) (*Factory, error) {
	if postCommitFailures == nil {
		return nil, errors.New("session factory: post-commit failure reporter is required")
	}
	return &Factory{
		postCommitFailures: postCommitFailures,
	}, nil
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return session.PluginName
}

// Create strictly decodes configuration and constructs the Session Store.
func (builder *Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := pluginfactory.ValidateCreateContext(createContext); err != nil {
		return nil, err
	}
	if err := pluginfactory.ValidateEmptyConfig(
		rawConfig,
		"session factory",
	); err != nil {
		return nil, err
	}
	return session.NewMemoryStore(
		session.MemoryStoreOptions{
			PostCommitFailures: builder.postCommitFailures,
		},
	)
}

var _ pluginfactory.Factory = (*Factory)(nil)
