package llmactor

import "github.com/gorenx/goren/llm"

// SetConfiguration replaces one Agent's submitted settings.
type SetConfiguration struct {
	AgentID string
	Config  llm.CallConfig
}

// ReadConfiguration requests the settings captured by a new model call.
type ReadConfiguration struct {
	AgentID string
}

// RemoveConfiguration explicitly deletes retained Agent settings. Agent closure does not send it.
type RemoveConfiguration struct {
	AgentID string
}

// ConfigurationResult returns a detached configuration or a mutation failure.
type ConfigurationResult struct {
	Config llm.CallConfig
	Err    error
}

// ReadCall requests configuration and registration from one mailbox turn.
type ReadCall struct{ AgentID string }

// CallSnapshot fixes execution settings and the provider binding without I/O.
type CallSnapshot struct {
	Config  llm.CallConfig
	Binding *llm.Binding
	Err     error
}
