package systemprompt

import (
	"maps"
	"slices"

	"github.com/gorenx/goren/llm"
)

// Clone detaches a resolved prompt so an execution owner can retain its snapshot.
func (assembly PromptAssembly) Clone() (PromptAssembly, error) {
	detached := PromptAssembly{
		Sections:  slices.Clone(assembly.Sections),
		Contexts:  slices.Clone(assembly.Contexts),
		Variables: maps.Clone(assembly.Variables),
	}
	if assembly.Tools != nil {
		detached.Tools = make([]llm.ToolSchema, len(assembly.Tools))
		for index, schema := range assembly.Tools {
			copied, err := detachToolSchema(schema)
			if err != nil {
				return PromptAssembly{}, err
			}
			detached.Tools[index] = copied
		}
	}
	return detached, nil
}
