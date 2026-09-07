package systemprompt

import "context"

// Snapshot fixes registration membership for one model request.
// Providers are immutable strategies; evaluation receives request data separately.
type Snapshot struct {
	layers    []promptLayerSnapshot
	toolOrder []string
}

// Assemble evaluates the captured definitions without reading registration state.
// A provider may register future definitions without changing this request.
func (captured Snapshot) Assemble(ctx context.Context, input AssembleContext) (PromptAssembly, error) {
	if err := validatePromptMutationContext(ctx); err != nil {
		return PromptAssembly{}, err
	}
	resolver := assemblyResolver{
		requestContext:  ctx,
		assemblyContext: input,
		layers:          captured.layers,
		toolOrder:       captured.toolOrder,
	}
	return resolver.resolve()
}
