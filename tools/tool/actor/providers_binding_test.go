package toolactor

import (
	"context"
	"testing"

	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/tools/tool"
)

type identifiedBinding struct {
	id tools.ProviderID
}

func (source *identifiedBinding) ProviderID() tools.ProviderID { return source.id }
func (*identifiedBinding) Open(context.Context) (tool.Implementation, error) {
	panic("resolving a binding must not acquire resources")
}

type bindingSource struct {
	entries map[tools.ProviderID]tool.Provider
}

func (source bindingSource) Provider(name string, expected tools.ProviderID) (tool.Provider, bool) {
	if name != "search" {
		return nil, false
	}
	selected, found := source.entries[expected]
	return selected, found
}

func TestProviderBindingSelectsSameNameByPublishedIdentity(t *testing.T) {
	first := &identifiedBinding{
		id: "first",
	}
	second := &identifiedBinding{
		id: "second",
	}
	bindings := &Providers{}
	if err := bindings.Install(nil, bindingSource{
		entries: map[tools.ProviderID]tool.Provider{
			first.id: first,
		},
	}, bindingSource{
		entries: map[tools.ProviderID]tool.Provider{
			second.id: second,
		},
	}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []*identifiedBinding{first, second} {
		selected, err := bindings.resolveBinding("search", expected.id, nil)
		if err != nil || selected != expected {
			t.Fatalf("wrong implementation for %s: %v", expected.id, err)
		}
	}
	if _, err := bindings.resolveBinding("search", "replaced", nil); err == nil {
		t.Fatal("missing exact identity fell back to another implementation")
	}
	if _, err := bindings.resolveBinding("search", "", nil); err == nil {
		t.Fatal("dynamic tool accepted without a published identity")
	}
}

func TestProviderBindingRejectsAmbiguousOrMismatchedSource(t *testing.T) {
	first := &identifiedBinding{
		id: "first",
	}
	duplicate := bindingSource{
		entries: map[tools.ProviderID]tool.Provider{
			first.id: first,
		},
	}
	bindings := &Providers{}
	if err := bindings.Install(nil, duplicate, duplicate); err != nil {
		t.Fatal(err)
	}
	if _, err := bindings.resolveBinding("search", first.id, nil); err == nil {
		t.Fatal("two providers claim the same published binding")
	}
	dishonest := &Providers{}
	if err := dishonest.Install(nil, bindingSource{
		entries: map[tools.ProviderID]tool.Provider{
			"other": first,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dishonest.resolveBinding("search", "other", nil); err == nil {
		t.Fatal("Provider source returned an unselected implementation")
	}
}
