package childpolicy

import (
	"context"
	"testing"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

func TestPluginsSeedDelegationOnlyForFreshChild(t *testing.T) {
	t.Parallel()
	personaText := "review carefully"
	delegation := &delegationRecord{}
	selected := PolicySet{
		Delegation: delegation,
		Persona:    &personaText,
		ToolRestriction: &tools.ToolRestriction{
			Allow: []string{"read"},
		},
	}
	fresh := Plugins(selected)
	selected.Delegation = nil
	resumed := Plugins(selected)
	if len(fresh) != 3 || len(resumed) != 2 {
		t.Fatalf(
			"child policy Plugins fresh=%d resumed=%d",
			len(fresh),
			len(resumed),
		)
	}
	if _, matches := fresh[0].(*delegationPolicy); !matches {
		t.Fatalf("first fresh Plugin = %T, want delegation policy", fresh[0])
	}
	if _, matches := resumed[0].(*persona); !matches {
		t.Fatalf("first resumed Plugin = %T, want persona", resumed[0])
	}
	if _, matches := resumed[1].(*toolRestriction); !matches {
		t.Fatalf("second resumed Plugin = %T, want Tool restriction", resumed[1])
	}
}

type delegationRecord struct {
	plugin.Base
}

func (*delegationRecord) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "test/delegation-policy",
	}
}

func (*delegationRecord) Apply(context.Context) error   { return nil }
func (*delegationRecord) Dispose(context.Context) error { return nil }
func (*delegationRecord) SeedDelegationPolicy(
	context.Context,
	session.Context,
) error {
	return nil
}
