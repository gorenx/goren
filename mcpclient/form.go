package mcpclient

import (
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/gorenx/goren/userquestions"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

type form struct {
	fields    []formField
	validator *jsonschema.Schema
}

type formField struct {
	name     string
	title    string
	kind     string
	detail   string
	required bool
	choices  []string
}

func newForm(input any) (*form, error) {
	rawValue, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var shape struct {
		Type       string                    `json:"type"`
		Properties map[string]map[string]any `json:"properties"`
		Required   []string                  `json:"required"`
	}
	if json.Unmarshal(rawValue, &shape) != nil ||
		shape.Type != "object" ||
		len(shape.Properties) > 32 {
		return nil, errors.New("unsupported elicitation schema")
	}
	var document map[string]any
	if err := json.Unmarshal(rawValue, &document); err != nil {
		return nil, err
	}
	for key := range document {
		if !slices.Contains([]string{"type", "properties", "required", "additionalProperties", "title", "description"}, key) {
			return nil, errors.New("unsupported elicitation keyword")
		}
	}
	for _, name := range shape.Required {
		if _, exists := shape.Properties[name]; !exists {
			return nil, errors.New("unknown required elicitation field")
		}
	}
	if additional, exists := document["additionalProperties"]; exists {
		if _, valid := additional.(bool); !valid {
			return nil, errors.New("additionalProperties must be a boolean")
		}
	}
	prepared := &form{}
	for name, property := range shape.Properties {
		lower := strings.ToLower(name)
		for _, sensitive := range []string{"password", "secret", "token", "credential", "api_key", "apikey"} {
			if strings.Contains(lower, sensitive) {
				return nil, errors.New("secret elicitation is unsupported")
			}
		}
		for key := range property {
			if !slices.Contains([]string{"type", "title", "description", "enum", "default", "format", "minLength", "maxLength", "pattern", "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf", "items", "minItems", "maxItems", "uniqueItems"}, key) {
				return nil, errors.New("unsupported elicitation property")
			}
		}
		if property["format"] == "password" {
			return nil, errors.New("secret elicitation is unsupported")
		}
		kind, _ := property["type"].(string)
		if kind != "array" && property["items"] != nil {
			return nil, errors.New("items requires an array field")
		}
		if !slices.Contains([]string{"string", "number", "integer", "boolean", "array"}, kind) {
			return nil, errors.New("unsupported elicitation type")
		}
		label, _ := property["title"].(string)
		if label == "" {
			label = name
		}
		detail, _ := json.Marshal(property)
		field := formField{
			name:     name,
			title:    label,
			kind:     kind,
			detail:   string(detail),
			required: slices.Contains(shape.Required, name),
		}
		options := property["enum"]
		if kind == "array" {
			items, valid := property["items"].(map[string]any)
			if !valid || items["type"] != "string" {
				return nil, errors.New("only enum string arrays are supported")
			}
			for key := range items {
				if key != "type" && key != "enum" {
					return nil, errors.New("unsupported array schema")
				}
			}
			options = items["enum"]
			if options == nil {
				return nil, errors.New("array choices are required")
			}
		}
		if options != nil {
			items, valid := options.([]any)
			if !valid || len(items) == 0 {
				return nil, errors.New("invalid elicitation enum")
			}
			for _, item := range items {
				textValue, valid := item.(string)
				if !valid {
					return nil, errors.New("only string enums are supported")
				}
				field.choices = append(field.choices, textValue)
			}
		}
		if kind == "boolean" {
			field.choices = []string{"true", "false"}
		}
		prepared.fields = append(prepared.fields, field)
	}
	compiled := jsonschema.NewCompiler()
	compiled.AssertFormat()
	if err := compiled.AddResource("form.json", document); err != nil {
		return nil, err
	}
	validator, err := compiled.Compile("form.json")
	if err != nil {
		return nil, err
	}
	prepared.validator = validator
	sort.Slice(prepared.fields, func(left int, right int) bool { return prepared.fields[left].name < prepared.fields[right].name })
	return prepared, nil
}

func (prepared *form) questionBatch() []userquestions.Question {
	result := make([]userquestions.Question, 0, len(prepared.fields))
	for _, field := range prepared.fields {
		detail := field.detail
		if !field.required {
			detail += "\nOptional: leave blank to omit."
		}
		question := userquestions.Question{
			ID:       field.name,
			Question: field.title,
			Detail:   &detail,
		}
		if len(field.choices) != 0 {
			options := make([]userquestions.Option, 0, len(field.choices))
			for _, label := range field.choices {
				options = append(options, userquestions.Option{
					Label: label,
				})
			}
			question.Options = &options
		}
		if field.kind == "array" {
			multiple := true
			question.MultiSelect = &multiple
		}
		result = append(result, question)
	}
	return result
}

func (prepared *form) answer(input userquestions.Answer) (map[string]any, error) {
	provided := make(map[string]userquestions.AnswerItem)
	for _, item := range input.Answers {
		if _, exists := provided[item.ID]; exists {
			return nil, errors.New("duplicate answer")
		}
		provided[item.ID] = item
	}
	content := make(map[string]any)
	for _, field := range prepared.fields {
		item, exists := provided[field.name]
		delete(provided, field.name)
		missing := !exists || (len(item.Selected) == 0 && item.Custom == nil)
		if field.kind == "array" &&
			field.required &&
			exists {
			missing = false
		}
		if missing {
			if field.required {
				return nil, errors.New("required answer missing")
			}
			continue
		}
		if item.Custom != nil && len(item.Selected) != 0 {
			return nil, errors.New("ambiguous answer")
		}
		if field.kind == "array" {
			if item.Custom != nil {
				return nil, errors.New("array requires choices")
			}
			choices := make([]any, 0, len(item.Selected))
			for _, label := range item.Selected {
				if !slices.Contains(field.choices, label) {
					return nil, errors.New("unknown choice")
				}
				choices = append(choices, label)
			}
			content[field.name] = choices
			continue
		}
		textValue := ""
		if item.Custom != nil {
			textValue = *item.Custom
		} else if len(item.Selected) == 1 {
			textValue = item.Selected[0]
		} else {
			return nil, errors.New("expected one answer")
		}
		if len(field.choices) != 0 && !slices.Contains(field.choices, textValue) {
			return nil, errors.New("unknown choice")
		}
		switch field.kind {
		case "string":
			content[field.name] = textValue
		case "boolean":
			if textValue != "true" && textValue != "false" {
				return nil, errors.New("invalid boolean")
			}
			content[field.name] = textValue == "true"
		case "integer", "number":
			var parsed json.Number
			trimmed := strings.TrimSpace(textValue)
			if strings.HasPrefix(trimmed, `"`) ||
				json.Unmarshal([]byte(trimmed), &parsed) != nil ||
				parsed == "" {
				return nil, errors.New("invalid number")
			}
			content[field.name] = parsed
		}
	}
	if len(provided) != 0 {
		return nil, errors.New("unknown answer field")
	}
	if err := prepared.validator.Validate(content); err != nil {
		return nil, errors.New("answer does not satisfy schema")
	}
	return content, nil
}
