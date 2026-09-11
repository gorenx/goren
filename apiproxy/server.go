package apiproxy

import (
	"context"
	"errors"
	"sync/atomic"

	commandapi "github.com/gorenx/goren/apiproxy/commands"
	credentialsapi "github.com/gorenx/goren/apiproxy/credentials"
	"github.com/gorenx/goren/apiproxy/directorypicker"
	"github.com/gorenx/goren/apiproxy/interaction"
	llmapi "github.com/gorenx/goren/apiproxy/llm"
	mcpapi "github.com/gorenx/goren/apiproxy/mcp"
	presetapi "github.com/gorenx/goren/apiproxy/preset"
	"github.com/gorenx/goren/apiproxy/rpc"
	sessionapi "github.com/gorenx/goren/apiproxy/session"
	sessionevents "github.com/gorenx/goren/apiproxy/session/events"
	settingsapi "github.com/gorenx/goren/apiproxy/settings"
	"github.com/gorenx/goren/apiproxy/stream"
	workspaceapi "github.com/gorenx/goren/apiproxy/workspace"
	usersettings "github.com/gorenx/goren/settings"
)

// Dependencies supplies domain capabilities; composition owns their lifecycle.
type Dependencies struct {
	DirectoryPicker directorypicker.Picker
	Sessions        sessionapi.Dependencies
	Search          sessionapi.Search
	Snapshots       sessionevents.Snapshots
	Workspaces      workspaceapi.Directory
	Commands        commandapi.Resolver
	Credentials     credentialsapi.Provider
	Description     HostDescriptionProvider
	Presets         presetapi.Roster
	Settings        usersettings.Settings
	MCP             mcpapi.Directory
}

// Options contains API behavior and transport buffering settings.
type Options struct {
	Sessions     sessionapi.Options
	Streams      stream.Options
	Interactions interaction.Options
	CanOpenPath  bool
}

// Server composes domain gateways and transport-neutral request dispatch.
// It owns API resources, never Agent lifetimes or Actor framework handles.
type Server struct {
	methods      *rpc.Catalog
	broker       *stream.Broker
	sessions     *sessionevents.Feed
	workspaces   *workspaceapi.Gateway
	interactions *interaction.Gateway
	closed       atomic.Bool
}

// NewServer registers the included API before any network listener is exposed.
func NewServer(ctx context.Context, ports Dependencies, settings Options) (*Server, error) {
	if ctx == nil {
		return nil, errors.New("apiproxy: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ports.Credentials == nil {
		return nil, errors.New("apiproxy: credentials are required")
	}
	sessions, err := sessionapi.NewGateway(ctx, ports.Sessions, settings.Sessions)
	if err != nil {
		return nil, err
	}
	searches, err := sessionapi.NewSearchGateway(ports.Search, sessions)
	if err != nil {
		return nil, err
	}
	models, err := llmapi.NewGateway(ports.Sessions.Models)
	if err != nil {
		return nil, err
	}
	commands, err := commandapi.NewGateway(ports.Commands, sessions)
	if err != nil {
		return nil, err
	}
	broker, err := stream.NewBroker(settings.Streams)
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			broker.Close()
		}
	}()
	feed, err := sessionevents.NewFeed(ports.Snapshots, broker)
	if err != nil {
		return nil, err
	}
	workspaces, err := workspaceapi.NewGateway(ports.Workspaces, broker)
	if err != nil {
		return nil, err
	}
	methods := rpc.NewCatalog()
	if err := directorypicker.Register(methods, ports.DirectoryPicker); err != nil {
		return nil, err
	}
	if err := mcpapi.Register(methods, ports.MCP); err != nil {
		return nil, err
	}
	if err := RegisterHostDescribe(methods, ports.Description); err != nil {
		return nil, err
	}
	if err := sessionapi.Register(methods, sessions, searches); err != nil {
		return nil, err
	}
	if err := llmapi.Register(methods, models); err != nil {
		return nil, err
	}
	if err := commandapi.Register(methods, commands); err != nil {
		return nil, err
	}
	if err := workspaceapi.Register(methods, workspaces); err != nil {
		return nil, err
	}
	if err := credentialsapi.Register(methods, credentialsapi.NewGateway(ports.Credentials)); err != nil {
		return nil, err
	}
	if err := presetapi.Register(methods, presetapi.NewGateway(ports.Presets, presetapi.Options{
		CanOpenPath: settings.CanOpenPath,
	})); err != nil {
		return nil, err
	}
	if err := settingsapi.Register(methods, settingsapi.NewGateway(ports.Settings)); err != nil {
		return nil, err
	}
	interactions, err := interaction.NewGateway(interaction.Dependencies{
		Methods: methods,
		Frames:  broker,
	}, settings.Interactions)
	if err != nil {
		return nil, err
	}
	complete = true
	return &Server{
		methods:      methods,
		broker:       broker,
		sessions:     feed,
		workspaces:   workspaces,
		interactions: interactions,
	}, nil
}

// Close rejects new requests, settles human interactions, then closes downlinks.
// A canceled close may be retried; it does not close Agents or their Sessions.
func (owner *Server) Close(ctx context.Context) error {
	owner.closed.Store(true)
	if err := owner.interactions.Close(ctx); err != nil {
		return err
	}
	owner.broker.Close()
	return nil
}

// SessionEvents supplies the Session notification consumer to composition.
func (owner *Server) SessionEvents() *sessionevents.Feed { return owner.sessions }

// WorkspaceEvents supplies the Workspace notification consumer to composition.
func (owner *Server) WorkspaceEvents() *workspaceapi.Gateway { return owner.workspaces }

// Interactions supplies the question and approval provider to composition.
func (owner *Server) Interactions() *interaction.Gateway { return owner.interactions }

var _ Service = (*Server)(nil)
