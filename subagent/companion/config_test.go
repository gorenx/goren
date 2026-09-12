package companion_test

import (
	"github.com/gorenx/goren/subagent/companion"
	"testing"
)

func TestConfigurationRejectsInvalidInheritanceAndReference(t *testing.T) {
	for _, input := range []companion.Config{
		{
			Inherit: companion.Inheritance{
				Tools: "selected",
			},
		},
		{
			Configuration: " invalid ",
		},
	} {
		if input.Validate() == nil {
			t.Fatal("invalid creation policy accepted")
		}
	}
	for _, selection := range []companion.ToolInheritance{"", companion.InheritNoTools, companion.InheritAllTools} {
		if err := (companion.Config{
			Inherit: companion.Inheritance{
				Tools: selection,
			},
		}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
