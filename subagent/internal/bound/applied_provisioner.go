package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

func newAppliedProvisioner(
	definitionValue boundcontract.Definition,
) agent.Provisioner {
	return &appliedProvisioner{
		definition: definitionValue,
	}
}

// appliedProvisioner appends the complete effective Definition while the
// child Agent is still unpublished.
type appliedProvisioner struct {
	definition boundcontract.Definition
}

func (source *appliedProvisioner) Provision(
	requestContext context.Context,
	target agent.Scope,
) (agent.Provisioning, error) {
	if source == nil || target == nil || target.Agent() == nil ||
		target.Agent().SessionValue() == nil {
		return nil, errors.New(
			"subagent: applied Bound Definition target is unavailable",
		)
	}
	draft, err := session.NewEventDraft(
		boundcontract.DefinitionAppliedEvent,
		boundcontract.DefinitionAppliedData{
			Version:    boundcontract.EventVersion,
			Definition: source.definition,
		},
	)
	if err != nil {
		return nil, err
	}
	return &appliedProvisioning{
		ctx:          requestContext,
		conversation: target.Agent().SessionValue(),
		draft:        draft,
	}, nil
}

var _ agent.Provisioner = (*appliedProvisioner)(nil)
