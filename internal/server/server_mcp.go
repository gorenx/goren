package server

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/llm"
	toolregistry "github.com/gorenx/goren/tools/registry"
	registryactor "github.com/gorenx/goren/tools/registry/actor"
	"github.com/gorenx/goren/tools/tool"
	"github.com/gorenx/goren/userquestions"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func (owner *Server) initializeMCP(ctx context.Context, questions userquestions.UserQuestions) ([]tool.ProviderSource, error) {
	directory, err := goakt.GrainOf[*registryactor.RegistryGrain](ctx, owner.system, registryactor.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		return nil, err
	}
	owner.mcp = newMCPManager(owner.system, directory, owner.settings.Timeout, questions)
	payload, err := owner.system.AskGrain(ctx, owner.mcpIdentity, bindMCP{
		manager: owner.mcp,
	}, owner.settings.Timeout)
	if err != nil {
		return nil, err
	}
	result, valid := payload.(mcpReply)
	if !valid {
		return nil, errors.New("server: invalid MCP binding reply")
	}
	if result.err != nil {
		return nil, result.err
	}
	for _, settings := range owner.settings.MCP {
		if _, err := owner.mcp.connect(ctx, settings); err != nil {
			return nil, err
		}
	}
	return []tool.ProviderSource{owner.mcp}, nil
}

func publishMCP(ctx context.Context, system goakt.ActorSystem, directory *goakt.GrainIdentity, timeout time.Duration, name string, entries []llm.ToolSchema) error {
	payload, err := system.AskGrain(ctx, directory, registryactor.RegisterSource{
		Name: name,
		Catalog: toolregistry.Catalog{
			Schemas: entries,
		},
	}, timeout)
	if err != nil {
		return err
	}
	result, valid := payload.(registryactor.Completion)
	if !valid {
		return errors.New("server: invalid MCP catalog publication reply")
	}
	return result.Err
}
