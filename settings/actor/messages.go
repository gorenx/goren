// Package settingsactor adapts the Settings service to GoAkt and owns its
// database installation. Public messages contain only process-local capabilities
// and detached business values, never generated storage rows.
package settingsactor

import (
	"context"

	"github.com/gorenx/goren/settings"
	"github.com/gorenx/goren/subagent/companion"
	"github.com/gorenx/goren/tools"
)

// Initialize installs the uniquely owned service before network admission.
type Initialize struct {
	Service *settings.Service
}

// Describe reads the last completed, detached configuration snapshot.
type Describe struct{}

// UpdateProvider replaces one complete named group.
type UpdateProvider struct {
	Context          context.Context
	Name             string
	ExpectedRevision int64
	Config           settings.ProviderConfig
}

// ResetProvider clears one complete named override.
type ResetProvider struct {
	Context          context.Context
	Name             string
	ExpectedRevision int64
}

// UpdatePrompt replaces one complete named group.
type UpdatePrompt struct {
	Context          context.Context
	Name             string
	ExpectedRevision int64
	Config           settings.PromptConfig
}

// ResetPrompt clears one complete named override.
type ResetPrompt struct {
	Context          context.Context
	Name             string
	ExpectedRevision int64
}

// UpdateMCP replaces one complete named group.
type UpdateMCP struct {
	Context          context.Context
	Name             string
	ExpectedRevision int64
	Config           settings.MCPConfig
}

// ResetMCP clears one complete named override.
type ResetMCP struct {
	Context          context.Context
	Name             string
	ExpectedRevision int64
}

// Close stops new writes and waits for every admitted request to settle.
type Close struct{}

// Result carries the latest completed snapshot, including after a rejected edit.
type Result struct {
	Snapshot settings.Snapshot
	Err      error
}

// UpdateAgent saves a complete named configuration without owning an Agent lifetime.
type UpdateAgent struct {
	Context          context.Context
	Name             string
	ExpectedRevision int64
	Config           settings.Values
}

func (input UpdateAgent) context() context.Context { return input.Context }
func (input UpdateAgent) apply(owner *settings.Service) (settings.Snapshot, error) {
	return owner.UpdateAgent(input.Context, input.Name, input.ExpectedRevision, input.Config)
}

type command interface {
	context() context.Context
	apply(*settings.Service) (settings.Snapshot, error)
}

func (input UpdateProvider) context() context.Context { return input.Context }
func (input UpdateProvider) apply(owner *settings.Service) (settings.Snapshot, error) {
	return owner.UpdateProvider(input.Context, input.Name, input.ExpectedRevision, input.Config)
}

func (input ResetProvider) context() context.Context { return input.Context }
func (input ResetProvider) apply(owner *settings.Service) (settings.Snapshot, error) {
	return owner.ResetProvider(input.Context, input.Name, input.ExpectedRevision)
}

func (input UpdatePrompt) context() context.Context { return input.Context }
func (input UpdatePrompt) apply(owner *settings.Service) (settings.Snapshot, error) {
	return owner.UpdatePrompt(input.Context, input.Name, input.ExpectedRevision, input.Config)
}

func (input ResetPrompt) context() context.Context { return input.Context }
func (input ResetPrompt) apply(owner *settings.Service) (settings.Snapshot, error) {
	return owner.ResetPrompt(input.Context, input.Name, input.ExpectedRevision)
}

func (input UpdateMCP) context() context.Context { return input.Context }
func (input UpdateMCP) apply(owner *settings.Service) (settings.Snapshot, error) {
	return owner.UpdateMCP(input.Context, input.Name, input.ExpectedRevision, input.Config)
}

func (input ResetMCP) context() context.Context { return input.Context }
func (input ResetMCP) apply(owner *settings.Service) (settings.Snapshot, error) {
	return owner.ResetMCP(input.Context, input.Name, input.ExpectedRevision)
}

// UpdateTool replaces complete flags for one exposed tool.
type UpdateTool struct {
	Context          context.Context
	Name             string
	ExpectedRevision int64
	Config           tools.Config
}

// ResetTool clears the complete named tool override.
type ResetTool struct {
	Context          context.Context
	Name             string
	ExpectedRevision int64
}

func (input UpdateTool) context() context.Context { return input.Context }
func (input UpdateTool) apply(owner *settings.Service) (settings.Snapshot, error) {
	return owner.UpdateTool(input.Context, input.Name, input.ExpectedRevision, input.Config)
}

func (input ResetTool) context() context.Context { return input.Context }
func (input ResetTool) apply(owner *settings.Service) (settings.Snapshot, error) {
	return owner.ResetTool(input.Context, input.Name, input.ExpectedRevision)
}

// UpdateCompanion replaces one complete named group.
type UpdateCompanion struct {
	Context          context.Context
	Name             string
	ExpectedRevision int64
	Config           companion.Config
}

// ResetCompanion clears one complete named override.
type ResetCompanion struct {
	Context          context.Context
	Name             string
	ExpectedRevision int64
}

func (input UpdateCompanion) context() context.Context { return input.Context }
func (input UpdateCompanion) apply(owner *settings.Service) (settings.Snapshot, error) {
	return owner.UpdateCompanion(input.Context, input.Name, input.ExpectedRevision, input.Config)
}

func (input ResetCompanion) context() context.Context { return input.Context }
func (input ResetCompanion) apply(owner *settings.Service) (settings.Snapshot, error) {
	return owner.ResetCompanion(input.Context, input.Name, input.ExpectedRevision)
}

// ChangedTopic carries saved configuration snapshots through GoAkt PubSub.
// It is not a Session event and has no independent persistence or replay log.
const ChangedTopic = "settings/changed"

// ChangeNotice carries a saved configuration to lifecycle owners. Receiving a
// notice does not mutate running components; Agent Registry replaces affected lifetimes.
type ChangeNotice struct {
	Revision      int64
	configuration settings.Directory
}

// Configuration returns an isolated copy of the saved values.
func (notice ChangeNotice) Configuration() settings.Directory {
	return notice.configuration.Clone()
}
