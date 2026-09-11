package subagent

import (
	"context"

	"github.com/gorenx/goren/systemprompt"
	toolregistry "github.com/gorenx/goren/tools/registry"
)

// ExtensionOption configures one Extension registration. Its fields remain
// private so registration behavior stays closed to this package.
type ExtensionOption struct {
	name *string
}

// WithExtensionName makes one Extension selectable by a stable config name
// instead of installing it as a common Extension for every child.
func WithExtensionName(extensionNameValue string) ExtensionOption {
	return ExtensionOption{
		name: &extensionNameValue,
	}
}

// Name returns the configured stable name, when this option sets one.
func (option ExtensionOption) Name() (string, bool) {
	if option.name == nil {
		return "", false
	}
	return *option.name, true
}

// ExtensionRegistration owns one exact child Extension registration and its
// resident installations.
type ExtensionRegistration interface {
	Unregister(context.Context) error
}

// ChildConfiguration is the detached configuration assembled before the Agent
// Registry receives a child creation request.
type ChildConfiguration struct {
	PromptSections []systemprompt.PromptSection
	Tools          toolregistry.Catalog
}

// Extension contributes to one unpublished child configuration. It receives
// no Agent, Scope, Actor address, or lifecycle owner.
type Extension interface {
	ConfigureChild(context.Context, *ChildConfiguration) error
}

// ExtensionRegistry owns Extensions installed into unpublished and resident
// child Agents.
type ExtensionRegistry interface {
	RegisterExtension(
		Extension,
		...ExtensionOption,
	) (ExtensionRegistration, error)
}

// ExtensionDescriptor identifies one named Extension selectable by child
// configuration. Common Extensions are intentionally absent from this view.
type ExtensionDescriptor struct {
	Name string
}

// ExtensionDirectory provides the current selectable Extension catalog
// without exposing registration or installation behavior.
type ExtensionDirectory interface {
	ListExtensions() []ExtensionDescriptor
}
