package toolaskuser

import (
	"context"
	"errors"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/tools/tool"
	"github.com/gorenx/goren/userquestions"
)

// Provider creates independent tool bodies that borrow the question capability.
type Provider struct {
	questions userquestions.UserQuestions
}

// NewProvider binds an initialized question capability without opening tool work.
func NewProvider(questions userquestions.UserQuestions) (*Provider, error) {
	if questions == nil {
		return nil, errors.New("toolaskuser: User Questions service is required")
	}
	return &Provider{
		questions: questions,
	}, nil
}

// Open creates one implementation; the borrowed question service outlives it.
func (source *Provider) Open(ctx context.Context) (tool.Implementation, error) {
	if ctx == nil {
		return tool.Implementation{}, errors.New("toolaskuser: context is required")
	}
	err := ctx.Err()
	if err != nil {
		return tool.Implementation{}, err
	}
	definition, err := NewDefinition(source.questions)
	if err != nil {
		return tool.Implementation{}, err
	}
	return tool.Implementation{
		Definition: definition,
	}, nil
}

// Schema returns a detached model definition without constructing tool work.
func Schema() llm.ToolSchema {
	return llm.ToolSchema{
		Name:        Name,
		Description: Description,
		Parameters:  append([]byte(nil), parameterSchema...),
	}
}

var _ tool.Provider = (*Provider)(nil)
