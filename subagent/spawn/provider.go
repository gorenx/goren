// Package spawn provides fresh in-process Subagent children.
package spawn

import (
	"context"
	"errors"
	"strings"

	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/inprocess"
)

const (
	// PluginName is the canonical spawn Provider Plugin name.
	PluginName = "@deepseek-ai/dsh-subagent-spawn-in-process"
	// DefaultProviderName is the default Provider registry identity.
	DefaultProviderName = "spawn"
)

// Provider is the pure spawn business strategy. Plugin activation and
// Provider registration belong to Plugin.
type Provider struct {
	name   string
	driver *inprocess.Driver
}

func newProvider(providerName string, driver *inprocess.Driver) (*Provider, error) {
	if err := validateProviderName(providerName); err != nil {
		return nil, err
	}
	if driver == nil {
		return nil, errors.New("subagent: spawn Provider requires an in-process Driver")
	}
	return &Provider{
		name:   providerName,
		driver: driver,
	}, nil
}

func (owner *Provider) Name() string {
	return owner.name
}

func (*Provider) Capabilities() subagent.Capabilities {
	return subagent.Capabilities{
		OutputSchema: true,
		DepthLimit:   true,
		ToolFilter:   true,
		Persona:      true,
	}
}

func (*Provider) InheritsParentContext() bool {
	return false
}

func (owner *Provider) Start(
	requestContext context.Context,
	request subagent.ResolvedStartRequest,
) (subagent.Run, error) {
	return owner.driver.Start(
		requestContext,
		request,
		inprocess.Options{},
	)
}

func (*Provider) PrepareContinuable(
	requestContext context.Context,
	_ subagent.ContinuableCreateRequest,
) (subagent.ContinuableCreateSpec, error) {
	if requestContext == nil {
		return subagent.ContinuableCreateSpec{}, errors.New(
			"subagent: spawn preparation context is nil",
		)
	}
	return subagent.ContinuableCreateSpec{}, requestContext.Err()
}

func validateProviderName(providerName string) error {
	if strings.TrimSpace(providerName) == "" ||
		providerName != strings.TrimSpace(providerName) {
		return errors.New(
			"subagent: spawn providerName must be non-empty and trimmed",
		)
	}
	return nil
}

var _ subagent.ContinuableProvider = (*Provider)(nil)
