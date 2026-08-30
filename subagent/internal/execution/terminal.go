package execution

import (
	"encoding/json"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/subagent"
)

// CloneTerminal returns a detached copy of a mode-owned terminal value.
func CloneTerminal(source subagent.Terminal) subagent.Terminal {
	detached := source
	detached.Output, _ = agentmessage.CloneContentBlocks(source.Output)
	detached.Structured = append(json.RawMessage(nil), source.Structured...)
	if source.Diagnostic != nil {
		diagnosticValue := *source.Diagnostic
		detached.Diagnostic = &diagnosticValue
	}
	return detached
}
