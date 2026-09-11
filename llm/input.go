package llm

import "github.com/gorenx/goren/agentmessage"

// Input is the conversation supplied for one generation. Configuration comes
// from a separate configuration choice, not from conversation content.
type Input struct {
	Messages  []agentmessage.Message
	System    *string
	Tools     []ToolSchema
	SessionID string
	Purpose   CallPurpose
}

// Options combines this input with the call's fixed configuration and detaches
// all mutable values before they cross the provider boundary.
func (input Input) Options(settings CallConfig) (GenerateOptions, error) {
	return CloneGenerateOptions(GenerateOptions{
		CallConfig: settings,
		Messages:   input.Messages,
		System:     input.System,
		Tools:      input.Tools,
		SessionID:  input.SessionID,
		Purpose:    input.Purpose,
	})
}
