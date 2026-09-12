// Package mcpclient connects one configured external MCP Server to Goren's
// Tool capability without exposing SDK or transport types as Runtime services.
package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/client"
	sdktransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

const protocolVersion = mcp.ProtocolVersion20260728

type protocolSettings struct {
	skipDiscover       bool
	identity           mcp.Implementation
	elicitation        client.ElicitationHandler
	requestTimeout     time.Duration
	toolResultLimit    int
	maxInputRoundTrips int
}

type protocolSession struct {
	client          *client.Client
	identity        mcp.Implementation
	toolResultLimit int
}

func newProtocolSession(
	wire sdktransport.Interface,
	settings protocolSettings,
) (*protocolSession, error) {
	if settings.identity.Name == "" || settings.identity.Version == "" {
		return nil, errors.New("mcpclient: client identity is required")
	}
	if settings.toolResultLimit <= 0 {
		return nil, errors.New("mcpclient: Tool result byte limit must be positive")
	}
	if settings.maxInputRoundTrips <= 0 {
		return nil, errors.New("mcpclient: MRTR round limit must be positive")
	}
	wrapped, err := newProtocolTransport(wire, settings.requestTimeout, settings.skipDiscover)
	if err != nil {
		return nil, err
	}
	options := []client.ClientOption{
		client.WithProtocolVersion(protocolVersion),
		client.WithMaxInputRoundTrips(settings.maxInputRoundTrips),
	}
	if settings.elicitation != nil {
		options = append(
			options,
			client.WithClientCapabilities(mcp.ClientCapabilities{
				Elicitation: &mcp.ElicitationCapability{
					Form: &struct{}{},
					URL:  &struct{}{},
				},
			}),
			client.WithElicitationHandler(settings.elicitation),
		)
	}
	return &protocolSession{
		client:          client.NewClient(wrapped, options...),
		identity:        settings.identity,
		toolResultLimit: settings.toolResultLimit,
	}, nil
}

func (session *protocolSession) start(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("mcpclient: start Context is nil")
	}
	if err := session.client.Start(requestContext); err != nil {
		return fmt.Errorf("mcpclient: start transport: %w", err)
	}
	initialized, err := session.client.Initialize(
		requestContext,
		mcp.InitializeRequest{
			Params: mcp.InitializeParams{
				ProtocolVersion: protocolVersion,
				ClientInfo:      session.identity,
			},
		},
	)
	if err != nil {
		_ = session.client.Close()
		return fmt.Errorf("mcpclient: discover Server: %w", err)
	}
	if initialized.ProtocolVersion != protocolVersion ||
		session.client.ProtocolVersion() != protocolVersion {
		_ = session.client.Close()
		return fmt.Errorf(
			"mcpclient: unsupported protocol version %q; require %s",
			initialized.ProtocolVersion,
			protocolVersion,
		)
	}
	return nil
}

func (session *protocolSession) listToolsPage(
	requestContext context.Context,
	cursor string,
) (*mcp.ListToolsResult, error) {
	request := mcp.ListToolsRequest{}
	request.Params.Cursor = mcp.Cursor(cursor)
	return session.client.ListToolsByPage(requestContext, request)
}

func (session *protocolSession) callTool(
	requestContext context.Context,
	name string,
	arguments json.RawMessage,
) (json.RawMessage, error) {
	if requestContext == nil {
		return nil, errors.New("mcpclient: call Tool Context is nil")
	}
	captureContext, capture := contextWithToolResultCapture(
		requestContext,
		session.toolResultLimit,
	)
	result, err := session.client.CallTool(
		captureContext,
		mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Name:         name,
				RawArguments: append(json.RawMessage(nil), arguments...),
			},
		},
	)
	rawResult, captureErr := capture.readResult()
	if captureErr != nil {
		if errors.Is(captureErr, errToolResultNotCaptured) && err != nil {
			return nil, err
		}
		return nil, captureErr
	}
	if err != nil {
		if completeErr := requireCompleteToolResult(rawResult); completeErr != nil {
			return nil, err
		}
		return rawResult, nil
	}
	if result == nil {
		return nil, errors.New("mcpclient: MCP SDK returned a nil Tool result")
	}
	if completeErr := requireCompleteToolResult(rawResult); completeErr != nil {
		return nil, completeErr
	}
	return rawResult, nil
}

func (session *protocolSession) listen(
	requestContext context.Context,
	filter mcp.SubscriptionFilter,
) error {
	return session.client.Listen(requestContext, filter)
}

func (session *protocolSession) onNotification(
	handler func(mcp.JSONRPCNotification),
) {
	session.client.OnNotification(handler)
}

func (session *protocolSession) close() error {
	return session.client.Close()
}
