package apiproxy

import (
	"context"

	boundcontract "github.com/gorenx/goren/subagent/bound"
)

const (
	// BoundListMethod lists committed global Bound Definitions.
	BoundListMethod = "bound.list"
	// BoundCreateMethod creates one globally named Bound Definition.
	BoundCreateMethod = "bound.create"
	// BoundReplaceMethod replaces one complete Definition by revision CAS.
	BoundReplaceMethod = "bound.replace"
)

// BoundListRequest is the empty bound.list payload.
type BoundListRequest struct{}

// BoundListValue contains the complete committed Definition roster.
type BoundListValue struct {
	Definitions []boundcontract.Definition `json:"definitions"`
}

// BoundDefinitionValue returns the exact committed Definition revision.
type BoundDefinitionValue struct {
	Definition boundcontract.Definition `json:"definition"`
}

// BoundAPI owns the browser-facing global Definition methods.
type BoundAPI interface {
	List(context.Context, Request[BoundListRequest]) (Outcome[BoundListValue], error)
	Create(context.Context, Request[boundcontract.Creation]) (Outcome[BoundDefinitionValue], error)
	Replace(context.Context, Request[boundcontract.Replacement]) (Outcome[BoundDefinitionValue], error)
}

// RegisterBoundAPI installs the complete global Bound Definition surface.
func RegisterBoundAPI(methods *Catalog, gateway BoundAPI) error {
	if err := RegisterUnary(
		methods,
		BoundListMethod,
		DecodeObject[BoundListRequest],
		gateway.List,
	); err != nil {
		return err
	}
	if err := RegisterUnary(
		methods,
		BoundCreateMethod,
		DecodeObject[boundcontract.Creation],
		gateway.Create,
	); err != nil {
		return err
	}
	return RegisterUnary(
		methods,
		BoundReplaceMethod,
		DecodeObject[boundcontract.Replacement],
		gateway.Replace,
	)
}
