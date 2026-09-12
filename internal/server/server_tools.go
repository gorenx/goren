package server

import (
	"context"
	"slices"

	"github.com/gorenx/goren/approval"
	approvalactor "github.com/gorenx/goren/approval/actor"
	approvaltool "github.com/gorenx/goren/approval/tool"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/toolaskuser"
	toolregistry "github.com/gorenx/goren/tools/registry"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	"github.com/gorenx/goren/tools/tool"
	toolactor "github.com/gorenx/goren/tools/tool/actor"
	"github.com/gorenx/goren/userquestions"
	questionactor "github.com/gorenx/goren/userquestions/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// defaultTools fills missing process defaults while preserving explicit schemas
// and policies. Session overrides are still merged exclusively by Tool Runtime.
func defaultTools(custom toolregistry.Catalog) toolregistry.Catalog {
	entries := custom.Clone()
	for _, definition := range []llm.ToolSchema{toolaskuser.Schema(), approvaltool.Schema()} {
		found := slices.ContainsFunc(entries.Schemas, func(existing llm.ToolSchema) bool {
			return existing.Name == definition.Name
		})
		if !found {
			entries.Schemas = append(entries.Schemas, definition)
		}
	}
	if entries.Policies == nil {
		entries.Policies = make(map[string]toolregistry.Policy)
	}
	policy := entries.Policies[approval.ServiceName]
	if policy.Visible == nil {
		hidden := false
		policy.Visible = &hidden
	}
	entries.Policies[approval.ServiceName] = policy
	return entries
}

func (owner *Server) initializeToolProviders(ctx context.Context, sessions *goakt.GrainIdentity, constructors *toolactor.Providers) error {
	system := owner.system
	timeout := owner.settings.Timeout
	authority, err := questionactor.NewDirectory(system, owner.agents, timeout)
	if err != nil {
		return err
	}
	questions := userquestions.New(owner.api.Interactions(), authority)
	if err := runtimeactor.Install(system, questions); err != nil {
		return err
	}
	asking, err := toolaskuser.NewProvider(questions)
	if err != nil {
		return err
	}
	logs, err := approvalactor.NewSessions(system, sessions, timeout)
	if err != nil {
		return err
	}
	settings, err := approval.ValidateConfig(approval.Config{})
	if err != nil {
		return err
	}
	permissions, err := approvaltool.NewProvider(settings, logs, owner.api.Interactions())
	if err != nil {
		return err
	}
	entries := tool.Providers{
		toolaskuser.Name:     asking,
		approval.ServiceName: permissions,
	}
	for name, source := range owner.settings.Providers {
		entries[name] = source
	}
	sources, err := owner.initializeMCP(ctx, questions)
	if err != nil {
		return err
	}
	return constructors.Install(entries, sources...)
}
