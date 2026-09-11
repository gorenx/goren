package server

import (
	"context"
	"errors"
	"slices"
	"time"

	mcpapi "github.com/gorenx/goren/apiproxy/mcp"
	"github.com/gorenx/goren/mcpclient"
	usersettings "github.com/gorenx/goren/settings"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// mcpDirectory holds only the GoAkt address, keeping API/elicitation acyclic.
type mcpDirectory struct {
	system   goakt.ActorSystem
	identity *goakt.GrainIdentity
}

var _ usersettings.MCPConnections = (*mcpDirectory)(nil)

// Reconcile implements the Settings capability through the existing MCP owner.
// Its wait follows caller cancellation; individual connections keep their own
// SDK deadlines and pending user questions are not prematurely timed out here.
func (source *mcpDirectory) Reconcile(ctx context.Context, definitions []mcpclient.Config) error {
	return source.sendConfiguration(ctx, definitions, false)
}

// Validate checks the MCP owner's local constraints before Settings commits.
func (source *mcpDirectory) Validate(ctx context.Context, definitions []mcpclient.Config) error {
	return source.sendConfiguration(ctx, definitions, true)
}

func (source *mcpDirectory) sendConfiguration(ctx context.Context, definitions []mcpclient.Config, validation bool) error {
	if ctx == nil {
		return errors.New("server: MCP context is required")
	}
	captured := slices.Clone(definitions)
	for index := range captured {
		captured[index].Args = slices.Clone(captured[index].Args)
	}
	var message any = mcpConfiguration{
		ctx:         ctx,
		definitions: captured,
	}
	if validation {
		message = mcpValidation{
			ctx:         ctx,
			definitions: captured,
		}
	}
	payload, err := source.system.AskGrain(ctx, source.identity, message, time.Duration(1<<63-1))
	if err != nil {
		return err
	}
	result, valid := payload.(mcpReply)
	if !valid {
		return errors.New("server: invalid MCP configuration reply")
	}
	return result.err
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
