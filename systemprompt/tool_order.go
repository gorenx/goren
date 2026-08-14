package systemprompt

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/gorenx/goren/llm"
)

func validateToolOrder(requested []string) ([]string, error) {
	if requested == nil {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(requested))
	for _, name := range requested {
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("systemprompt: toolOrder lists %q more than once", name)
		}
		seen[name] = struct{}{}
	}
	if _, exists := seen[ToolOrderRest]; !exists {
		return nil, fmt.Errorf("systemprompt: toolOrder must contain the %q rest entry (where unlisted tools are inserted)", ToolOrderRest)
	}
	return slices.Clone(requested), nil
}

func orderToolSchemas(schemas []llm.ToolSchema, requested []string, knownNames map[string]struct{}) ([]llm.ToolSchema, error) {
	for _, schema := range schemas {
		if schema.Name == ToolOrderRest {
			return nil, fmt.Errorf("systemprompt: tool provider returned reserved tool name %q (reserved for toolOrder's rest entry)", ToolOrderRest)
		}
	}
	if requested == nil {
		sort.SliceStable(schemas, func(leftIndex int, rightIndex int) bool {
			return schemas[leftIndex].Name < schemas[rightIndex].Name
		})
		return schemas, nil
	}
	unknown := make([]string, 0)
	listed := make(map[string]struct{}, len(requested))
	for _, name := range requested {
		listed[name] = struct{}{}
		if name != ToolOrderRest {
			if _, exists := knownNames[name]; !exists {
				unknown = append(unknown, name)
			}
		}
	}
	if len(unknown) > 0 {
		known := make([]string, 0, len(knownNames))
		for name := range knownNames {
			known = append(known, name)
		}
		sort.Strings(known)
		quoted := make([]string, len(unknown))
		for index, name := range unknown {
			quoted[index] = fmt.Sprintf("%q", name)
		}
		label := "tool"
		if len(unknown) > 1 {
			label = "tools"
		}
		knownLabel := "(none)"
		if len(known) > 0 {
			knownLabel = strings.Join(known, ", ")
		}
		return nil, fmt.Errorf("systemprompt: toolOrder lists unregistered %s %s; known tools: %s", label, strings.Join(quoted, ", "), knownLabel)
	}
	rest := make([]llm.ToolSchema, 0)
	for _, schema := range schemas {
		if _, exists := listed[schema.Name]; !exists {
			rest = append(rest, schema)
		}
	}
	sort.SliceStable(rest, func(leftIndex int, rightIndex int) bool {
		return rest[leftIndex].Name < rest[rightIndex].Name
	})
	ordered := make([]llm.ToolSchema, 0, len(schemas))
	for _, name := range requested {
		if name == ToolOrderRest {
			ordered = append(ordered, rest...)
			continue
		}
		for _, schema := range schemas {
			if schema.Name == name {
				ordered = append(ordered, schema)
			}
		}
	}
	return ordered, nil
}

func detachToolSchema(schema llm.ToolSchema) (llm.ToolSchema, error) {
	trimmed := strings.TrimSpace(string(schema.Parameters))
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return llm.ToolSchema{}, fmt.Errorf("systemprompt: tool %q parameters must be a JSON object", schema.Name)
	}
	if !jsonValidObject(schema.Parameters) {
		return llm.ToolSchema{}, fmt.Errorf("systemprompt: tool %q parameters must be a valid JSON object", schema.Name)
	}
	return llm.ToolSchema{
		Name: schema.Name, Description: schema.Description, Parameters: slices.Clone(schema.Parameters),
	}, nil
}

func jsonValidObject(raw []byte) bool {
	var decoded map[string]json.RawMessage
	return json.Unmarshal(raw, &decoded) == nil && decoded != nil
}
