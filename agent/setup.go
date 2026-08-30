package agent

import "context"

// Setup declares one cohesive set of Agent-local capabilities. Apply writes
// only to the supplied private Scope draft; it never mounts a Plugin.
type Setup interface {
	Apply(context.Context, Agent, ScopeEditor) error
}

// ComposeSetups returns one ordered Setup. A nil result means there is no
// Agent-local contribution to apply.
func ComposeSetups(sources ...Setup) Setup {
	selected := make([]Setup, 0, len(sources))
	for _, source := range sources {
		if source != nil {
			selected = append(selected, source)
		}
	}
	if len(selected) == 0 {
		return nil
	}
	return &composedSetup{
		sources: selected,
	}
}

type composedSetup struct {
	sources []Setup
}

func (owner *composedSetup) Apply(
	requestContext context.Context,
	subject Agent,
	editor ScopeEditor,
) error {
	for _, source := range owner.sources {
		if err := source.Apply(requestContext, subject, editor); err != nil {
			return err
		}
	}
	return nil
}

var _ Setup = (*composedSetup)(nil)
