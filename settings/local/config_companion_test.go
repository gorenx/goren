package local_test

import (
	"reflect"
	"testing"

	"github.com/gorenx/goren/settings/local"
	"github.com/gorenx/goren/subagent/companion"
)

func TestCompanionJSONAndYAMLUseTypedConfiguration(t *testing.T) {
	isolateConfigEnvironment(t)
	root := t.TempDir()
	yamlPath := writeConfig(t, root, "companion.yaml", `schemaVersion: 1
agents:
  review-config:
    model:
      model: review-model
      maxTokens: 400
    prompts:
      default:
        persona: Review changes
        includeRuntimeContext: false
    tools:
      read:
        visible: false
companions:
  reviewer:
    enabled: true
    configuration: review-config
    inherit:
      tools: all
      mcp: true
`)
	jsonPath := writeConfig(t, root, "companion.json", `{"schemaVersion":1,"agents":{"review-config":{"model":{"model":"review-model","maxTokens":400},"prompts":{"default":{"persona":"Review changes","includeRuntimeContext":false}},"tools":{"read":{"visible":false}}}},"companions":{"reviewer":{"enabled":true,"configuration":"review-config","inherit":{"tools":"all","mcp":true}}}}`)
	fromYAML, err := local.Load(yamlPath, local.Config{})
	if err != nil {
		t.Fatal(err)
	}
	fromJSON, err := local.Load(jsonPath, local.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromYAML, fromJSON) {
		t.Fatal("YAML and JSON typed configurations differ")
	}
	selected := fromYAML.Agents[fromYAML.Companions["reviewer"].Configuration]
	if !fromYAML.Companions["reviewer"].Enabled || selected.Model.MaxTokens == nil || *selected.Model.MaxTokens != 400 || selected.Prompts["default"].IncludeRuntimeContext == nil || *selected.Prompts["default"].IncludeRuntimeContext || selected.Tools["read"].Visible == nil || *selected.Tools["read"].Visible {
		t.Fatal("koanf lost typed values or explicit false")
	}
	for _, content := range []string{
		`{"schemaVersion":1,"companions":{"reviewer":{"inherit":{"tools":"some"}}}}`,
		`{"schemaVersion":1,"companions":{"reviewer":{"model":{"MaxTokens":400}}}}`,
		`{"schemaVersion":1,"companions":{"reviewer":{"enabled":"true"}}}`,
		`{"schemaVersion":1,"companions":{"reviewer":{"binding":"old"}}}`,
	} {
		path := writeConfig(t, root, "invalid.json", content)
		if _, err := local.LoadFile(path); err == nil {
			t.Fatal("invalid typed Companion configuration accepted")
		}
	}
}

func TestCompanionReferenceCanSelectConfigurationFromLowerLocalSource(t *testing.T) {
	isolateConfigEnvironment(t)
	path := writeConfig(t, t.TempDir(), "base.yaml", `schemaVersion: 1
agents:
  review-config:
    prompts:
      default:
        persona: From file
`)
	loaded, err := local.Load(path, local.Config{
		Companions: map[string]companion.Config{
			"reviewer": {
				Enabled:       true,
				Configuration: "review-config",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	selected := loaded.Agents[loaded.Companions["reviewer"].Configuration]
	if selected.Prompts["default"].Persona == nil || *selected.Prompts["default"].Persona != "From file" {
		t.Fatal("koanf source precedence lost the referenced configuration")
	}
}
