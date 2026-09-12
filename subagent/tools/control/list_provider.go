package control

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools/tool"
)

// Listing identifies the calling parent and the requested directory traversal.
type Listing struct {
	// ParentSessionID comes from the trusted tool invocation, never model arguments.
	ParentSessionID session.SessionID
	// Scope selects children or descendants; an omitted tool argument selects children.
	Scope string
}

// Entry is one model-facing directory row, not an Agent or lifecycle owner.
type Entry struct {
	// Kind selects the existing child or diagnostic output branch.
	Kind string `json:"kind"`
	// ID identifies the durable child Session.
	ID string `json:"id"`
	// Label presents the child name on non-diagnostic rows.
	Label string `json:"label,omitempty"`
	// Status reports observed execution or lifecycle state on child rows.
	Status string `json:"status,omitempty"`
	// Reason contains a bounded diagnostic category, never an internal error message.
	Reason string `json:"reason,omitempty"`
	// Parent identifies the direct parent when a descendant traversal requests it.
	Parent *string `json:"parent,omitempty"`
	// Depth is the distance from the caller in a descendant traversal.
	Depth *int64 `json:"depth,omitempty"`
}

// Lister owns directory interpretation, admission and lifecycle observation.
// Implementations must not start or resume Agents as a side effect of listing.
type Lister interface {
	List(context.Context, Listing) ([]Entry, error)
}

// ListProvider supplies the shared list_agents tool using a borrowed directory.
type ListProvider struct {
	// source outlives tool bodies and retains its own lifecycle responsibility.
	source Lister
}

// NewListProvider binds direct-child listing independently of other control tools.
func NewListProvider(source Lister) (*ListProvider, error) {
	if source == nil {
		return nil, errors.New("list_agents requires a Lister")
	}
	return &ListProvider{
		source: source,
	}, nil
}

// Open creates a tool body without reading or modifying directory state.
func (source *ListProvider) Open(ctx context.Context) (tool.Implementation, error) {
	if ctx == nil {
		return tool.Implementation{}, errors.New("list_agents requires a context")
	}
	if err := ctx.Err(); err != nil {
		return tool.Implementation{}, err
	}
	body := &listTool{
		source: source.source,
	}
	return tool.Implementation{
		Definition: body.definition(ListSchema()),
	}, nil
}

// ListSchema returns a detached schema without opening a tool body.
func ListSchema() llm.ToolSchema {
	return llm.ToolSchema{
		Name:        "list_agents",
		Description: "List the calling Agent's directly managed background agents by durable id, name and observed state. This query does not start or resume Agents.",
		Parameters:  json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
	}
}

var _ tool.Provider = (*ListProvider)(nil)
