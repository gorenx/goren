// Package apiactor adapts API capability queries to the Actor system.
package apiactor

import (
	"context"
	"errors"
	"github.com/google/uuid"
	llmapi "github.com/gorenx/goren/apiproxy/llm"
	"time"

	"github.com/gorenx/goren/llm"
	llmactor "github.com/gorenx/goren/llm/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Directory implements the API's read-only model capability. These methods run
// on the inbound request goroutine, never inside an Actor mailbox.
type Directory struct {
	system   goakt.ActorSystem
	registry *goakt.GrainIdentity
	queries  *goakt.GrainIdentity
	timeout  time.Duration
}

// NewDirectory binds existing LLM endpoints; it creates no actors or providers.
func NewDirectory(system goakt.ActorSystem, registry *goakt.GrainIdentity, queries *goakt.GrainIdentity, timeout time.Duration) (*Directory, error) {
	if system == nil ||
		registry == nil ||
		queries == nil ||
		timeout <= 0 {
		return nil, errors.New("apiproxy: model directory requires Actor endpoints and a positive timeout")
	}
	return &Directory{
		system:   system,
		registry: registry,
		queries:  queries,
		timeout:  timeout,
	}, nil
}

// ReadCatalog reads active and configurable provider metadata atomically.
func (source *Directory) ReadCatalog(ctx context.Context) (llmapi.ProviderCatalog, error) {
	bounded, cancel := context.WithTimeout(ctx, source.timeout)
	defer cancel()
	payload, err := source.system.AskGrain(bounded, source.registry, llmactor.ReadCatalog{}, source.timeout)
	if err != nil {
		return llmapi.ProviderCatalog{}, err
	}
	result, ok := payload.(llmactor.Catalog)
	if !ok {
		return llmapi.ProviderCatalog{}, errors.New("apiproxy: invalid provider catalog reply")
	}
	return llmapi.ProviderCatalog{
		Active:       result.Providers,
		Configurable: result.Configurable,
	}, nil
}

// ListModels requests provider metadata through the Router query bridge.
func (source *Directory) ListModels(ctx context.Context, provider string) ([]llm.ModelInfo, error) {
	bounded, cancel := context.WithTimeout(ctx, source.timeout)
	defer cancel()
	deadline, _ := bounded.Deadline()
	id := llmactor.RequestID(uuid.NewString())
	payload, err := source.query(bounded, id, llmactor.ListModels{
		Query: llmactor.Query{
			RequestID: id,
			Deadline:  deadline,
		},
		Provider: provider,
	})
	if err != nil {
		return nil, err
	}
	result, ok := payload.(llmactor.ModelsListed)
	if !ok {
		return nil, errors.New("apiproxy: invalid model catalog reply")
	}
	return result.Models, result.Err
}

// ResolveModelInfo queries the selected model's exact capabilities.
func (source *Directory) ResolveModelInfo(ctx context.Context, provider string, modelID string) (llm.ResolvedModelInfo, error) {
	bounded, cancel := context.WithTimeout(ctx, source.timeout)
	defer cancel()
	deadline, _ := bounded.Deadline()
	id := llmactor.RequestID(uuid.NewString())
	payload, err := source.query(bounded, id, llmactor.ResolveModel{
		Query: llmactor.Query{
			RequestID: id,
			Deadline:  deadline,
		},
		Provider: provider,
		Model:    modelID,
	})
	if err != nil {
		return llm.ResolvedModelInfo{}, err
	}
	result, ok := payload.(llmactor.ModelResolved)
	if !ok {
		return llm.ResolvedModelInfo{}, errors.New("apiproxy: invalid model capability reply")
	}
	return result.Info, result.Err
}

// ResolveCallConfig validates a selection without updating execution settings.
func (source *Directory) ResolveCallConfig(ctx context.Context, proposed llm.CallConfig) (llm.CallConfig, error) {
	bounded, cancel := context.WithTimeout(ctx, source.timeout)
	defer cancel()
	deadline, _ := bounded.Deadline()
	id := llmactor.RequestID(uuid.NewString())
	payload, err := source.query(bounded, id, llmactor.ResolveConfig{
		Query: llmactor.Query{
			RequestID: id,
			Deadline:  deadline,
		},
		Config: llm.CloneCallConfig(proposed),
	})
	if err != nil {
		return llm.CallConfig{}, err
	}
	result, ok := payload.(llmactor.ConfigResolved)
	if !ok {
		return llm.CallConfig{}, errors.New("apiproxy: invalid model configuration reply")
	}
	return result.Config, result.Err
}

var _ llmapi.Directory = (*Directory)(nil)

func (source *Directory) query(ctx context.Context, id llmactor.RequestID, command any) (any, error) {
	result, err := source.system.AskGrain(ctx, source.queries, command, source.timeout)
	if err != nil && ctx.Err() != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), source.timeout)
		defer cancel()
		delivery := source.system.TellGrain(cleanup, source.queries, llmactor.CancelQuery{
			RequestID: id,
			Cause:     context.Cause(ctx),
		})
		return nil, errors.Join(err, delivery)
	}
	return result, err
}
