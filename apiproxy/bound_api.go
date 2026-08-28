package apiproxy

import (
	"context"

	boundcontract "github.com/gorenx/goren/subagent/bound"
)

const (
	// BoundListMethod lists committed global Bound Definitions.
	BoundListMethod = "bound.list"
	// BoundToolsMethod lists root tools that a Bound restriction may inherit.
	BoundToolsMethod = "bound.tools"
	// BoundExtensionsMethod lists named Extensions selectable by a Bound.
	BoundExtensionsMethod = "bound.extensions"
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

// BoundToolsRequest is the empty bound.tools payload.
type BoundToolsRequest struct{}

// BoundToolOption is one root Tool available to Bound restriction selection.
type BoundToolOption struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// BoundToolsValue contains the current root Tool choices.
type BoundToolsValue struct {
	Tools []BoundToolOption `json:"tools"`
}

// BoundExtensionsRequest is the empty bound.extensions payload.
type BoundExtensionsRequest struct{}

// BoundExtensionOption is one named Extension selectable by Bound config.
type BoundExtensionOption struct {
	Name string `json:"name"`
}

// BoundExtensionsValue contains the current named Extension choices.
type BoundExtensionsValue struct {
	Extensions []BoundExtensionOption `json:"extensions"`
}

// BoundDefinitionValue returns the exact committed Definition revision.
type BoundDefinitionValue struct {
	Definition boundcontract.Definition `json:"definition"`
}

// BoundAPI owns the browser-facing global Definition methods.
type BoundAPI interface {
	List(context.Context, Request[BoundListRequest]) (Outcome[BoundListValue], error)
	Tools(context.Context, Request[BoundToolsRequest]) (Outcome[BoundToolsValue], error)
	Extensions(context.Context, Request[BoundExtensionsRequest]) (Outcome[BoundExtensionsValue], error)
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
		BoundToolsMethod,
		DecodeObject[BoundToolsRequest],
		gateway.Tools,
	); err != nil {
		return err
	}
	if err := RegisterUnary(
		methods,
		BoundExtensionsMethod,
		DecodeObject[BoundExtensionsRequest],
		gateway.Extensions,
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
