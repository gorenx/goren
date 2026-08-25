// Package fork provides in-process Subagent children seeded from a parent.
package fork

import (
	"context"
	"errors"
	"strings"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/inprocess"
)

const (
	// PluginName is the canonical fork Provider Plugin name.
	PluginName = "@deepseek-ai/dsh-subagent-fork-in-process"
	// DefaultProviderName is the default Provider registry identity.
	DefaultProviderName = "fork"
)

// Provider is the pure fork business strategy. Plugin activation and
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
		return nil, errors.New("subagent: fork Provider requires an in-process Driver")
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
	return true
}

func (owner *Provider) Start(
	requestContext context.Context,
	request subagent.ResolvedStartRequest,
) (subagent.Run, error) {
	return owner.driver.Start(
		requestContext,
		request,
		inprocess.Options{
			Seed: completedTurnPrefix(request.Parent),
		},
	)
}

func (*Provider) PrepareContinuable(
	requestContext context.Context,
	request subagent.ContinuableCreateRequest,
) (subagent.ContinuableCreateSpec, error) {
	if requestContext == nil {
		return subagent.ContinuableCreateSpec{}, errors.New(
			"subagent: fork preparation context is nil",
		)
	}
	if requestErr := requestContext.Err(); requestErr != nil {
		return subagent.ContinuableCreateSpec{}, requestErr
	}
	if request.Parent == nil {
		return subagent.ContinuableCreateSpec{}, errors.New(
			"subagent: fork preparation requires a parent Agent",
		)
	}
	return subagent.ContinuableCreateSpec{
		Seed: completedTurnPrefix(request.Parent),
	}, nil
}

func completedTurnPrefix(parentAgent agent.Agent) []session.Event {
	if parentAgent == nil || parentAgent.SessionValue() == nil {
		return nil
	}
	events := parentAgent.SessionValue().Events()
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type == session.TurnEndEventName {
			return events[:index+1]
		}
	}
	return nil
}

func validateProviderName(providerName string) error {
	if strings.TrimSpace(providerName) == "" ||
		providerName != strings.TrimSpace(providerName) {
		return errors.New(
			"subagent: fork providerName must be non-empty and trimmed",
		)
	}
	return nil
}

var _ subagent.ContinuableProvider = (*Provider)(nil)
