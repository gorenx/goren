package server

import (
	"context"
	"errors"
	"time"

	mcpapi "github.com/gorenx/goren/apiproxy/mcp"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// mcpDirectory holds only the GoAkt address, keeping API/elicitation acyclic.
type mcpDirectory struct {
	system   goakt.ActorSystem
	identity *goakt.GrainIdentity
}

func (source *mcpDirectory) ConnectHTTP(ctx context.Context, input mcpapi.ConnectRequest) (mcpapi.View, error) {
	result, err := source.sendManagement(ctx, "connect", input)
	if err != nil {
		return mcpapi.View{}, err
	}
	if len(result.views) != 1 {
		return mcpapi.View{}, errors.New("server: invalid MCP connection reply")
	}
	return result.views[0], nil
}

func (source *mcpDirectory) ListConnections(ctx context.Context) ([]mcpapi.View, error) {
	result, err := source.sendManagement(ctx, "list", mcpapi.ConnectRequest{})
	return result.views, err
}

func (source *mcpDirectory) Disconnect(ctx context.Context, name string) error {
	_, err := source.sendManagement(ctx, "disconnect", mcpapi.ConnectRequest{
		ServerName: name,
	})
	return err
}

func (source *mcpDirectory) sendManagement(ctx context.Context, operation string, input mcpapi.ConnectRequest) (mcpReply, error) {
	// Management has its own finite whole-operation deadline; Tool calls retain
	// their independently configured MRTR and elicitation lifetimes.
	bounded, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	payload, err := source.system.AskGrain(bounded, source.identity, mcpCommand{
		ctx:       bounded,
		operation: operation,
		input:     input,
	}, 35*time.Second)
	if err != nil {
		return mcpReply{}, err
	}
	result, valid := payload.(mcpReply)
	if !valid {
		return mcpReply{}, errors.New("server: invalid MCP management reply")
	}
	return result, result.err
}
