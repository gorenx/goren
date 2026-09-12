package systemprompt

import (
	"context"
	"fmt"
	"slices"
)

// Snapshot fixes registration membership for one model request.
// Providers are immutable strategies; evaluation receives request data separately.
type Snapshot struct {
	// baseline is configuration content, separate from registered definitions.
	baseline  *promptLayerSnapshot
	layers    []promptLayerSnapshot
	toolOrder []string
}

// Assemble evaluates the captured definitions without reading registration state.
// A provider may register future definitions without changing this request.
func (captured Snapshot) Assemble(ctx context.Context, input AssembleContext) (PromptAssembly, error) {
	if err := validatePromptMutationContext(ctx); err != nil {
		return PromptAssembly{}, err
	}
	layers := make([]promptLayerSnapshot, 0, len(captured.layers)+1)
	if captured.baseline != nil {
		layers = append(layers, *captured.baseline)
	}
	layers = append(layers, captured.layers...)
	resolver := assemblyResolver{
		requestContext:  ctx,
		assemblyContext: input,
		layers:          layers,
		toolOrder:       captured.toolOrder,
	}
	return resolver.resolve()
}

// WithConfiguration returns a snapshot using the supplied configuration and the
// same registered definitions. It does not mutate a Registry, a layer, or earlier
// snapshots. Root-name conflicts remain errors; child-layer overrides are retained.
func (captured Snapshot) WithConfiguration(input Config) (Snapshot, error) {
	validated, err := ValidateConfig(input)
	if err != nil {
		return Snapshot{}, err
	}
	baseline := validated.definitions()
	if len(captured.layers) != 0 {
		for _, entry := range baseline.sections {
			for _, registered := range captured.layers[0].sections {
				if entry.Name == registered.Name {
					return Snapshot{}, fmt.Errorf("systemprompt: prompt section %q is already registered", entry.Name)
				}
			}
		}
	}
	return Snapshot{
		baseline:  &baseline,
		layers:    slices.Clone(captured.layers),
		toolOrder: slices.Clone(validated.toolOrder),
	}, nil
}
