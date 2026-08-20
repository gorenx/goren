package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/internal/jsonvalue"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

type registeredTool struct {
	definition       ToolDefinition
	parameterSchema  *jsonschema.Schema
	outputSchema     *jsonschema.Schema
	registrationName string
}

func compileDefinition(definition ToolDefinition) (*registeredTool, error) {
	if strings.TrimSpace(definition.Name) == "" || definition.Name != strings.TrimSpace(definition.Name) {
		return nil, errors.New("tools: tool name must be non-empty and trimmed")
	}
	if definition.Name == RunCodeName {
		return nil, fmt.Errorf("tools: tool name %q is reserved for the Code Mode presentation transport", RunCodeName)
	}
	if definition.Executor == nil {
		return nil, fmt.Errorf("tools: tool %q executor is nil", definition.Name)
	}
	if definition.Output.Renderer == nil {
		return nil, fmt.Errorf("tools: tool %q must declare an output renderer", definition.Name)
	}
	if definition.Timeout < 0 {
		return nil, fmt.Errorf("tools: tool %q timeout must be positive", definition.Name)
	}
	parameters, err := jsonvalue.Clone(definition.Parameters)
	if err != nil {
		return nil, fmt.Errorf("tools: tool %q parameters are not lossless JSON: %w", definition.Name, err)
	}
	if !jsonvalue.IsObject(parameters) {
		return nil, fmt.Errorf("tools: tool %q parameters must be a JSON object schema", definition.Name)
	}
	var parameterFields map[string]json.RawMessage
	if err := json.Unmarshal(parameters, &parameterFields); err != nil {
		return nil, fmt.Errorf("tools: tool %q parameters: %w", definition.Name, err)
	}
	var schemaType string
	if err := json.Unmarshal(parameterFields["type"], &schemaType); err != nil || schemaType != "object" {
		return nil, fmt.Errorf("tools: tool %q parameters schema must declare type object", definition.Name)
	}
	output, err := jsonvalue.Clone(definition.Output.Schema)
	if err != nil {
		return nil, fmt.Errorf("tools: tool %q output schema is not lossless JSON: %w", definition.Name, err)
	}
	parameterValidator, err := compileSchema(definition.Name+"-parameters", parameters)
	if err != nil {
		return nil, fmt.Errorf("tools: tool %q parameters schema: %w", definition.Name, err)
	}
	outputValidator, err := compileSchema(definition.Name+"-output", output)
	if err != nil {
		return nil, fmt.Errorf("tools: tool %q output schema: %w", definition.Name, err)
	}
	retained := definition
	retained.Parameters = parameters
	retained.Output.Schema = output
	return &registeredTool{
		definition:       retained,
		parameterSchema:  parameterValidator,
		outputSchema:     outputValidator,
		registrationName: definition.Name,
	}, nil
}

func compileSchema(label string, rawValue json.RawMessage) (*jsonschema.Schema, error) {
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawValue))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	location := "mem://goren/tools/" + label + ".json"
	if err := compiler.AddResource(location, document); err != nil {
		return nil, err
	}
	return compiler.Compile(location)
}

func validateSchemaValue(validator *jsonschema.Schema, rawValue json.RawMessage, path string) error {
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawValue))
	if err != nil {
		return err
	}
	if err := validator.Validate(value); err != nil {
		var validationFailure *jsonschema.ValidationError
		if errors.As(err, &validationFailure) {
			leaf := deepestSchemaFailure(validationFailure)
			location := path
			if len(leaf.InstanceLocation) > 0 {
				location += "/" + strings.Join(leaf.InstanceLocation, "/")
			}
			return fmt.Errorf("%s: %s", location, leaf.Error())
		}
		return err
	}
	return nil
}

func deepestSchemaFailure(validationFailure *jsonschema.ValidationError) *jsonschema.ValidationError {
	deepest := validationFailure
	for _, cause := range validationFailure.Causes {
		candidate := deepestSchemaFailure(cause)
		if len(candidate.InstanceLocation) >= len(deepest.InstanceLocation) {
			deepest = candidate
		}
	}
	return deepest
}

func cloneDefinition(source ToolDefinition) ToolDefinition {
	detached := source
	detached.Parameters = append(json.RawMessage(nil), source.Parameters...)
	detached.Output.Schema = append(json.RawMessage(nil), source.Output.Schema...)
	return detached
}
