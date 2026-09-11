package settingsapi

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"

	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/subagent/companion"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// The API validates its published schema before decoding the capability's typed
// configuration. Local files use koanf; Companion has no separate decoder.
var companionRequestSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	document, err := jsonschema.UnmarshalJSON(strings.NewReader(companionSchema))
	if err != nil {
		return nil, err
	}
	builder := jsonschema.NewCompiler()
	const location = "mem://goren/settings/companion.json"
	if err := builder.AddResource(location, document); err != nil {
		return nil, err
	}
	return builder.Compile(location)
})

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
	validator, err := companionRequestSchema()
	if err != nil {
		return UpdateCompanionRequest{}, invalidSettingsRequest()
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(fields["config"]))
	if err != nil || validator.Validate(value) != nil {
		return UpdateCompanionRequest{}, invalidSettingsRequest()
	}
	var configuration companion.Config
	if json.Unmarshal(fields["config"], &configuration) != nil || configuration.Validate() != nil {
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
