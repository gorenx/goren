package toolruntime

import (
	"errors"
	"slices"
	"sort"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/tools/execution"
	toolregistry "github.com/gorenx/goren/tools/registry"
)

// Tool is one effective schema and policy after inheritance has been resolved.
// Disabled entries are absent; visibility does not remove execution membership.
type Tool struct {
	Schema     llm.ToolSchema
	ProviderID tools.ProviderID
	Visible    bool
	Policies   execution.Policies
}

// Merge resolves Session schemas by name and policy fields by explicit presence.
// Registry input remains unchanged; returned schemas own their parameter bytes.
func Merge(input toolregistry.Configuration) ([]Tool, error) {
	if len(input.Custom.ProviderIDs) != 0 {
		return nil, errors.New("tools: Session configuration cannot select Providers")
	}
	captured := toolregistry.Configuration{
		Global: input.Global.Clone(),
		Custom: input.Custom.Clone(),
	}
	// schemas maps a canonical tool name to its selected schema; a Session entry
	// replaces the global value before enablement and visibility policy is applied.
	schemas := make(map[string]llm.ToolSchema)
	for _, entry := range captured.Global.Schemas {
		schemas[entry.Name] = entry
	}
	for _, entry := range captured.Custom.Schemas {
		schemas[entry.Name] = entry
	}
	for name := range captured.Custom.Policies {
		if _, exists := schemas[name]; !exists {
			return nil, errors.New("tools: custom policy references an unknown schema")
		}
	}
	effective := make([]Tool, 0, len(schemas))
	for name, entry := range schemas {
		if !admitted(name, captured.Custom.Restriction) {
			continue
		}
		enabled := true
		visible := true
		policies := execution.Policies{}
		for _, policy := range []toolregistry.Policy{captured.Global.Policies[name], captured.Custom.Policies[name]} {
			if policy.Enabled != nil {
				enabled = *policy.Enabled
			}
			if policy.Visible != nil {
				visible = *policy.Visible
			}
			if policy.Before != nil {
				policies.Before = policy.Before
			}
			if policy.After != nil {
				policies.After = policy.After
			}
			if policy.Execution != nil {
				policies.Execution = policy.Execution
			}
			if policy.Results != nil {
				policies.Results = policy.Results
			}
			if policy.Guards != nil {
				policies.Guards = slices.Clone(policy.Guards)
			}
		}
		if enabled {
			effective = append(effective, Tool{
				Schema:     entry,
				ProviderID: captured.Global.ProviderIDs[name],
				Visible:    visible,
				Policies:   policies,
			})
		}
	}
	sort.Slice(effective, func(left int, right int) bool { return effective[left].Schema.Name < effective[right].Schema.Name })
	return effective, nil
}

func admitted(name string, restriction *toolregistry.ToolRestriction) bool {
	if restriction == nil {
		return true
	}
	if restriction.Allow != nil && !slices.Contains(restriction.Allow, name) {
		return false
	}
	return !slices.Contains(restriction.Deny, name)
}

func cloneTools(entries []Tool) []Tool {
	entries = slices.Clone(entries)
	for index := range entries {
		entries[index].Schema.Parameters = slices.Clone(entries[index].Schema.Parameters)
		entries[index].Policies.Guards = slices.Clone(entries[index].Policies.Guards)
	}
	return entries
}
