package companionapi

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"

	companions "github.com/gorenx/goren/subagent/companion"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// ConfigSchema is the published canonical HTTP shape of Companion policy.
const ConfigSchema = `{
 "$schema":"https://json-schema.org/draft/2020-12/schema",
 "type":"object","additionalProperties":false,
 "properties":{
  "enabled":{"type":"boolean"},
  "configuration":{"type":"string","minLength":1},
  "inherit":{"type":"object","additionalProperties":false,"properties":{
   "model":{"type":"boolean"},"prompt":{"type":"boolean"},
   "tools":{"enum":["none","all"]},"mcp":{"type":"boolean"}
  }}
 }
}`

var configSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	document, err := jsonschema.UnmarshalJSON(strings.NewReader(ConfigSchema))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	const location = "mem://goren/companion/config.json"
	if err := compiler.AddResource(location, document); err != nil {
		return nil, err
	}
	return compiler.Compile(location)
})

// DecodeConfig validates the same published shape for Settings and management.
func DecodeConfig(raw json.RawMessage) (companions.Config, error) {
	validator, err := configSchema()
	if err != nil {
		return companions.Config{}, companions.ErrInvalid
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil || validator.Validate(value) != nil {
		return companions.Config{}, companions.ErrInvalid
	}
	var configuration companions.Config
	if json.Unmarshal(raw, &configuration) != nil || configuration.Validate() != nil {
		return companions.Config{}, companions.ErrInvalid
	}
	return configuration, nil
}
