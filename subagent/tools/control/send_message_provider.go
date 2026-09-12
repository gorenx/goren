package control

import (
	"context"
	"errors"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/tools/tool"
)

// MessageProvider constructs send_message tools using a borrowed Sender.
type MessageProvider struct {
	delivery Sender
}

// NewMessageProvider binds sending without requiring other control capabilities.
func NewMessageProvider(delivery Sender) (*MessageProvider, error) {
	if delivery == nil {
		return nil, errors.New("send_message requires a Sender")
	}
	return &MessageProvider{
		delivery: delivery,
	}, nil
}

// Open creates an independent tool body; the borrowed Sender outlives it.
func (source *MessageProvider) Open(ctx context.Context) (tool.Implementation, error) {
	if ctx == nil {
		return tool.Implementation{}, errors.New("send_message requires a context")
	}
	if err := ctx.Err(); err != nil {
		return tool.Implementation{}, err
	}
	body := &messageTool{
		children: source.delivery,
	}
	return tool.Implementation{
		Definition: body.definition(),
	}, nil
}

// MessageSchema reads the send_message schema without constructing tool work.
func MessageSchema() llm.ToolSchema {
	return llm.ToolSchema{
		Name:        "send_message",
		Description: messageDescription,
		Parameters:  append([]byte(nil), sendMessageParameters...),
	}
}

var _ tool.Provider = (*MessageProvider)(nil)
