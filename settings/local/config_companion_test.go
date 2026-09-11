package local_test

import (
	"reflect"
	"testing"

	"github.com/gorenx/goren/settings/local"
)

func TestCompanionJSONAndYAMLUseTypedConfiguration(t *testing.T) {
	isolateConfigEnvironment(t)
	root := t.TempDir()
	yamlPath := writeConfig(t, root, "companion.yaml", `schemaVersion: 1
companions:
  reviewer:
    enabled: true
    inherit:
      tools: all
      mcp: true
    model:
      model: review-model
      maxTokens: 400
    prompt:
      persona: Review changes
      includeRuntimeContext: false
    tools:
      read:
        visible: false
`)
	jsonPath := writeConfig(t, root, "companion.json", `{"schemaVersion":1,"companions":{"reviewer":{"enabled":true,"inherit":{"tools":"all","mcp":true},"model":{"model":"review-model","maxTokens":400},"prompt":{"persona":"Review changes","includeRuntimeContext":false},"tools":{"read":{"visible":false}}}}}`)
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
	selected := fromYAML.Companions["reviewer"]
	if !selected.Enabled || selected.Model.MaxTokens == nil || *selected.Model.MaxTokens != 400 || selected.Prompt.IncludeRuntimeContext == nil || *selected.Prompt.IncludeRuntimeContext || selected.Tools["read"].Visible == nil || *selected.Tools["read"].Visible {
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
