package server

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/gorenx/goren/mcpclient"
	toolregistry "github.com/gorenx/goren/tools/registry"
	registryactor "github.com/gorenx/goren/tools/registry/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// mcpSettings publishes configuration values only. Session Tool Runtimes own I/O.
type mcpSettings struct {
	system   goakt.ActorSystem
	registry *goakt.GrainIdentity
	timeout  time.Duration
	local    []mcpclient.Config
	reserved []mcpclient.Config
}

func (source mcpSettings) ValidateMCP(ctx context.Context, definitions []mcpclient.Config) error {
	if ctx == nil {
		return errors.New("server: MCP configuration context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	names := make(map[string]struct{}, len(source.reserved)+len(definitions))
	for _, definition := range source.reserved {
		names[definition.ServerName] = struct{}{}
	}
	for _, definition := range definitions {
		configuration, err := definition.Validate()
		if err != nil {
			return err
		}
		if configuration.Transport != "streamable-http" {
			return errors.New("server: editable MCP configuration requires HTTP")
		}
		if _, found := names[configuration.ServerName]; found {
			return errors.New("server: duplicate or reserved MCP connection name")
		}
		names[configuration.ServerName] = struct{}{}
	}
	return nil
}

func (source mcpSettings) ApplyMCP(ctx context.Context, definitions []mcpclient.Config) error {
	if err := source.ValidateMCP(ctx, definitions); err != nil {
		return err
	}
	configuration := toolregistry.Catalog{
		MCP: append(slices.Clone(source.local), definitions...),
	}
	payload, err := source.system.AskGrain(ctx, source.registry, registryactor.RegisterSource{
		Name:    "settings.mcp",
		Catalog: configuration.Clone(),
	}, source.timeout)
	if err != nil {
		return err
	}
	result, valid := payload.(registryactor.Completion)
	if !valid {
		return errors.New("server: invalid MCP configuration publication reply")
	}
	return result.Err
}
