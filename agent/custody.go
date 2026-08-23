package agent

import (
	"context"
	"errors"

	"github.com/gorenx/goren/plugin"
)

type custodyContextKey struct{}

// Custody is one opaque structural ownership relationship for Agent trees.
// Plugin assembly creates it once; Agent consumers can bind calls to it without
// receiving Plugin topology authority.
type Custody struct {
	parent plugin.Plugin
}

// NewCustody creates an Agent custody backed by one Plugin activation.
func NewCustody(parent plugin.Plugin) (Custody, error) {
	if parent == nil || parent.RuntimePlugin() == nil {
		return Custody{}, errors.New("agent: Custody Plugin is required")
	}
	return Custody{
		parent: parent,
	}, nil
}

// Bind returns a request Context carrying this exact Agent custody.
func (binding Custody) Bind(requestContext context.Context) context.Context {
	if requestContext == nil {
		return nil
	}
	if binding.parent == nil {
		return requestContext
	}
	return context.WithValue(requestContext, custodyContextKey{}, binding)
}

// IsZero reports that no structural Agent custody was assigned.
func (binding Custody) IsZero() bool {
	return binding.parent == nil
}

func custodyFrom(requestContext context.Context) plugin.Plugin {
	if requestContext == nil {
		return nil
	}
	binding, _ := requestContext.Value(custodyContextKey{}).(Custody)
	return binding.parent
}
