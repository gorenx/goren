package report

import (
	"context"
	"errors"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/tools/tool"
)

// Provider constructs report tools using a borrowed parent-delivery capability.
type Provider struct {
	reporting  Reporter
	scheduling Delivery
}

// NewProvider fixes scheduling without constructing or registering a tool.
func NewProvider(reporting Reporter, scheduling Delivery) (*Provider, error) {
	if reporting == nil || (scheduling != Quiet && scheduling != NextStep) {
		return nil, errors.New("subagent report: Reporter and valid Delivery are required")
	}
	return &Provider{
		reporting:  reporting,
		scheduling: scheduling,
	}, nil
}

// Open creates an independent report body and borrows the reporting capability.
func (source *Provider) Open(ctx context.Context) (tool.Implementation, error) {
	if ctx == nil {
		return tool.Implementation{}, errors.New("subagent report: context is required")
	}
	if err := ctx.Err(); err != nil {
		return tool.Implementation{}, err
	}
	body, err := newReportTool(source.reporting, source.scheduling)
	if err != nil {
		return tool.Implementation{}, err
	}
	return tool.Implementation{
		Definition: body.definition(),
	}, nil
}

// Schema reads the report definition without constructing tool work.
func Schema() llm.ToolSchema {
	return llm.ToolSchema{
		Name:        "report",
		Description: description,
		Parameters:  append([]byte(nil), reportParameters...),
	}
}

var _ tool.Provider = (*Provider)(nil)
