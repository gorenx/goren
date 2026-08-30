package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

func newAppliedSetup(definitionValue boundcontract.Definition) agent.Setup {
	return &appliedSetup{
		definition: definitionValue,
	}
}

// appliedSetup appends the effective Bound Definition after all preceding
// child-local Setup contributions succeed and before Agent publication.
type appliedSetup struct {
	definition boundcontract.Definition
}

func (contribution *appliedSetup) Apply(
	requestContext context.Context,
	subject agent.Agent,
	_ agent.ScopeEditor,
) error {
	if contribution == nil || subject == nil || subject.SessionValue() == nil {
		return errors.New(
			"subagent: applied Bound Definition target is unavailable",
		)
	}
	draft, err := session.NewEventDraft(
		boundcontract.DefinitionAppliedEvent,
		boundcontract.DefinitionAppliedData{
			Version:    boundcontract.EventVersion,
			Definition: contribution.definition,
		},
	)
	if err != nil {
		return err
	}
	_, err = subject.SessionValue().Commit(
		requestContext,
		session.Batch(draft),
	)
	return err
}

var _ agent.Setup = (*appliedSetup)(nil)
