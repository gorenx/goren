package companionactor

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/gorenx/goren/agent"
	agentregistry "github.com/gorenx/goren/agent/registry"
	llmactor "github.com/gorenx/goren/llm/actor"
	"github.com/gorenx/goren/settings"
	companions "github.com/gorenx/goren/subagent/companion"
	"github.com/gorenx/goren/subagent/tools/report"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Resolve freezes only explicitly requested parent configuration. Provider
// connections remain global; independent capability owners supply adopted values.
func (owner *Creator) Resolve(
	ctx context.Context,
	parent agentregistry.Creation,
	policy companions.Config,
	initial settings.Values,
) (settings.Values, error) {
	if owner == nil ||
		ctx == nil ||
		owner.System == nil ||
		owner.Registry == nil ||
		owner.Tools == nil {
		return settings.Values{}, companions.ErrInvalid
	}
	if err := policy.Validate(); err != nil {
		return settings.Values{}, err
	}
	current, err := ask[agentregistry.Ready](ctx, owner.System, owner.Registry, agentregistry.FindAgent{
		AgentID: parent.AgentID,
	})
	if err != nil || current.Err != nil {
		return settings.Values{}, errors.Join(err, current.Err)
	}
	if current.Creation != parent || current.Registration.Role == agent.CompanionRole {
		return settings.Values{}, agentregistry.ErrCreation
	}
	resolved := initial.Clone()
	if policy.Inherit.Model {
		if owner.Model == nil {
			return settings.Values{}, companions.ErrInvalid
		}
		selected, failure := ask[llmactor.ConfigurationResult](
			ctx,
			owner.System,
			owner.Model,
			llmactor.ReadConfiguration{
				AgentID: string(parent.AgentID),
			},
		)
		if failure != nil || selected.Err != nil {
			return settings.Values{}, errors.Join(failure, selected.Err)
		}
		inherited := agent.Options{
			Provider:  selected.Config.Provider,
			Model:     selected.Config.Model,
			MaxTokens: intPointer(selected.Config.MaxTokens),
		}
		if resolved.Model.Provider != "" {
			inherited.Provider = resolved.Model.Provider
		}
		if resolved.Model.Model != "" {
			inherited.Model = resolved.Model.Model
		}
		if resolved.Model.MaxTokens != nil {
			inherited.MaxTokens = intPointer(resolved.Model.MaxTokens)
		}
		resolved.Model = inherited
	}
	if policy.Inherit.Prompt {
		if _, chosen := resolved.Prompts["default"]; !chosen {
			target, failure := goakt.GrainOf[*promptactor.RuntimeGrain](
				ctx, owner.System,
				promptactor.RuntimeName(string(parent.AgentID)),
				goakt.WithLongLivedGrain(),
			)
			if failure != nil {
				return settings.Values{}, failure
			}
			selected, failure := ask[promptactor.RuntimeView](ctx, owner.System, target, promptactor.ReadRuntime{})
			if failure != nil || selected.Err != nil {
				return settings.Values{}, errors.Join(failure, selected.Err)
			}
			prompt := settings.PromptConfig{
				IncludeHarnessIdentity: boolPointer(selected.Config.IncludeHarnessIdentity),
				IncludeRuntimeContext:  boolPointer(selected.Config.IncludeRuntimeContext),
				Persona:                &selected.Config.Persona,
			}
			if selected.Config.ToolOrder != nil {
				order := slices.Clone(selected.Config.ToolOrder)
				prompt.ToolOrder = &order
			}
			if resolved.Prompts == nil {
				resolved.Prompts = make(map[string]settings.PromptConfig)
			}
			resolved.Prompts["default"] = prompt
		}
	}
	if policy.Inherit.Tools == companions.InheritAllTools || policy.Inherit.MCP {
		target, failure := goakt.GrainOf[*runtimeactor.Runtime](
			ctx,
			owner.System,
			runtimeactor.RuntimeName(string(parent.AgentID)),
			goakt.WithLongLivedGrain(),
		)
		if failure != nil {
			return settings.Values{}, failure
		}
		current, failure := ask[runtimeactor.RuntimeView](ctx, owner.System, target, runtimeactor.ReadRuntime{
			CreationID: parent.CreationID,
		})
		if failure != nil {
			return settings.Values{}, failure
		}
		if current.Err != nil {
			return settings.Values{}, current.Err
		}
		catalog := current.Snapshot.Configuration
		if policy.Inherit.MCP {
			if resolved.MCP == nil {
				resolved.MCP = make(map[string]settings.MCPConfig)
			}
			for _, connection := range append(catalog.Global.MCP, catalog.Custom.MCP...) {
				if _, exists := resolved.MCP[connection.ServerName]; exists {
					return settings.Values{}, fmt.Errorf("%w: duplicate MCP connection %q", companions.ErrInvalid, connection.ServerName)
				}
				resolved.MCP[connection.ServerName] = settings.MCPConfig{
					Enabled:    true,
					Connection: connection,
				}
			}
		}
		if policy.Inherit.Tools == companions.InheritAllTools {
			if failure := inheritTools(catalog, &resolved); failure != nil {
				return settings.Values{}, failure
			}
		}
	}
	current, err = ask[agentregistry.Ready](ctx, owner.System, owner.Registry, agentregistry.FindAgent{
		AgentID: parent.AgentID,
	})
	if err != nil || current.Err != nil {
		return settings.Values{}, errors.Join(err, current.Err)
	}
	if current.Creation != parent {
		return settings.Values{}, agentregistry.ErrCreation
	}
	return resolved, resolved.Validate()
}

func inheritTools(
	source toolregistry.Configuration,
	resolved *settings.Values,
) error {
	if resolved.Tools == nil {
		resolved.Tools = make(map[string]tools.Config)
	}
	for _, schema := range append(source.Global.Schemas, source.Custom.Schemas...) {
		if !selectedByParent(schema.Name, source.Custom.Restriction) {
			continue
		}
		if _, duplicate := resolved.Tools[schema.Name]; duplicate || schema.Name == report.Schema().Name {
			return fmt.Errorf("%w: duplicate inherited tool %q", companions.ErrInvalid, schema.Name)
		}
		enabled, visible := true, true
		for _, policy := range []toolregistry.Policy{
			source.Global.Policies[schema.Name],
			source.Custom.Policies[schema.Name],
		} {
			if policy.Enabled != nil {
				enabled = *policy.Enabled
			}
			if policy.Visible != nil {
				visible = *policy.Visible
			}
		}
		resolved.Tools[schema.Name] = tools.Config{
			Enabled: &enabled,
			Visible: &visible,
		}
	}
	return nil
}

func selectedByParent(name string, restriction *toolregistry.ToolRestriction) bool {
	if restriction == nil {
		return true
	}
	if restriction.Allow != nil && !slices.Contains(restriction.Allow, name) {
		return false
	}
	return !slices.Contains(restriction.Deny, name)
}

func boolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func intPointer(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
