package settingsapi

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"

	companionapi "github.com/gorenx/goren/apiproxy/companion"
	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/settings"
	"github.com/gorenx/goren/subagent/companion"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// DecodeUpdateCompanionRequest checks the complete group and canonical keys.
// Web-editable MCP connections remain HTTP-only, as in settings.updateMCP.
func DecodeUpdateCompanionRequest(encoded json.RawMessage) (UpdateCompanionRequest, []connection.ValidationIssue) {
	revision, fields, valid := requestFields(encoded, "name", "config")
	if !valid {
		return UpdateCompanionRequest{}, invalidSettingsRequest()
	}
	name, issues := rpc.RequiredString(fields, "name", true)
	if len(issues) != 0 || companion.ValidateName(name) != nil {
		return UpdateCompanionRequest{}, invalidSettingsRequest()
	}
	configuration, err := companionapi.DecodeConfig(fields["config"])
	if err != nil {
		return UpdateCompanionRequest{}, invalidSettingsRequest()
	}
	return UpdateCompanionRequest{
		Name:             name,
		ExpectedRevision: revision,
		Config:           configuration,
	}, nil
}

// DecodeResetCompanionRequest checks a whole-group reset with its saved revision.
func DecodeResetCompanionRequest(encoded json.RawMessage) (ResetCompanionRequest, []connection.ValidationIssue) {
	revision, fields, valid := requestFields(encoded, "name")
	if !valid {
		return ResetCompanionRequest{}, invalidSettingsRequest()
	}
	name, issues := rpc.RequiredString(fields, "name", true)
	if len(issues) != 0 || companion.ValidateName(name) != nil {
		return ResetCompanionRequest{}, invalidSettingsRequest()
	}
	return ResetCompanionRequest{
		Name:             name,
		ExpectedRevision: revision,
	}, nil
}

var agentRequestSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	document, err := jsonschema.UnmarshalJSON(strings.NewReader(agentSchema))
	if err != nil {
		return nil, err
	}
	builder := jsonschema.NewCompiler()
	const location = "mem://goren/settings/agent.json"
	if err := builder.AddResource(location, document); err != nil {
		return nil, err
	}
	return builder.Compile(location)
})

// DecodeUpdateAgentRequest permits capability values without creation metadata.
// Model input cannot submit inheritance, enablement or executable MCP transports.
func DecodeUpdateAgentRequest(encoded json.RawMessage) (UpdateAgentRequest, []connection.ValidationIssue) {
	revision, fields, valid := requestFields(encoded, "name", "config")
	if !valid {
		return UpdateAgentRequest{}, invalidSettingsRequest()
	}
	name, issues := rpc.RequiredString(fields, "name", true)
	if len(issues) != 0 || !(settings.Group{
		Section: settings.AgentSection,
		Name:    name,
	}).Valid() {
		return UpdateAgentRequest{}, invalidSettingsRequest()
	}
	validator, err := agentRequestSchema()
	if err != nil {
		return UpdateAgentRequest{}, invalidSettingsRequest()
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(fields["config"]))
	if err != nil || validator.Validate(value) != nil {
		return UpdateAgentRequest{}, invalidSettingsRequest()
	}
	var configuration settings.Values
	if json.Unmarshal(fields["config"], &configuration) != nil || configuration.Validate() != nil {
		return UpdateAgentRequest{}, invalidSettingsRequest()
	}
	return UpdateAgentRequest{
		Name:             name,
		ExpectedRevision: revision,
		Config:           configuration,
	}, nil
}
