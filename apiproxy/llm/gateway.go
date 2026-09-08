package llmapi

import (
	"context"
	"github.com/gorenx/goren/apiproxy/rpc"
)

// Gateway maps directory business results to the Host RPC contract.
type Gateway struct {
	catalog *Catalog
}

// NewGateway binds the directory capability without owning its lifecycle.
func NewGateway(source Directory) (*Gateway, error) {
	listing, err := NewCatalog(source)
	if err != nil {
		return nil, err
	}
	return &Gateway{
		catalog: listing,
	}, nil
}

// Providers returns the provider directory in declaration order.
func (owner *Gateway) Providers(ctx context.Context, _ rpc.Request[ProvidersRequest]) (rpc.Outcome[ProvidersValue], error) {
	result, err := owner.catalog.Providers(ctx)
	if err != nil {
		return rpc.Outcome[ProvidersValue]{}, err
	}
	return rpc.OK(result), nil
}

// Models returns available model groups and isolated provider failures.
func (owner *Gateway) Models(ctx context.Context, _ rpc.Request[ModelsRequest]) (rpc.Outcome[ModelsValue], error) {
	groups, failures, err := owner.catalog.List(ctx)
	if err != nil {
		return rpc.Outcome[ModelsValue]{}, err
	}
	return rpc.OK(ModelsValue{
		Groups:   groups,
		Failures: failures,
	}), nil
}
