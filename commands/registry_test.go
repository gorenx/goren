package commands

import "testing"

func TestRegistryDetachesDefinitionsAndProtectsReplacement(t *testing.T) {
	owner := &Registry{}
	supplied := Definition{
		Name:        "compact",
		Description: "Compact history",
		Input: &InputDescriptor{
			Hint: "no arguments",
		},
	}
	first, err := owner.Register(supplied)
	if err != nil {
		t.Fatal(err)
	}
	supplied.Input.Hint = "changed"
	first.Definition.Input.Hint = "also changed"
	resolved, found := owner.Find("compact")
	if !found || resolved.Definition.Input.Hint != "no arguments" {
		t.Fatal("registration aliases caller data")
	}
	if _, err := owner.Register(supplied); err == nil {
		t.Fatal("duplicate accepted")
	}
	if !owner.Unregister(first) {
		t.Fatal("registration not removed")
	}
	replacement, err := owner.Register(supplied)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID == first.ID || owner.Unregister(first) {
		t.Fatal("old identity removed replacement")
	}
	descriptors := owner.List()
	descriptors[0].Input.Hint = "mutated list"
	if owner.List()[0].Input.Hint == "mutated list" {
		t.Fatal("list aliases registry")
	}
}

func TestRegistryRejectsInvalidDefinitions(t *testing.T) {
	for _, supplied := range []Definition{
		{
			Name:        "Invalid",
			Description: "description",
		},
		{
			Name: "valid",
		},
		{
			Name:        "valid",
			Description: "description",
			Input: &InputDescriptor{
				Hint:   "attachments",
				Images: true,
			},
		},
	} {
		owner := &Registry{}
		if _, err := owner.Register(supplied); err == nil {
			t.Fatalf("accepted %#v", supplied)
		}
		if len(owner.List()) != 0 {
			t.Fatal("failed registration published")
		}
	}
}

func TestRegistrationIdentityDoesNotRepeatAcrossOwners(t *testing.T) {
	first := &Registry{}
	second := &Registry{}
	definition := Definition{
		Name:        "compact",
		Description: "Compact history",
	}
	old, err := first.Register(definition)
	if err != nil {
		t.Fatal(err)
	}
	current, err := second.Register(definition)
	if err != nil {
		t.Fatal(err)
	}
	if current.ID == old.ID || second.Unregister(old) {
		t.Fatal("old owner identity removed new registration")
	}
}
