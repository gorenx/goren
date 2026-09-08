package commands

import "testing"

func TestCatalogInheritsLiveDefaultsAndRestoresThemAfterWithdrawal(t *testing.T) {
	owner := &Catalog{}
	original, err := owner.Register("", Definition{
		Name:        "compact",
		Description: "global",
	})
	if err != nil {
		t.Fatal(err)
	}
	local, err := owner.Register("session-a", Definition{
		Name:        "compact",
		Description: "custom",
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := owner.Register("", Definition{
		Name:        "compact",
		Description: "updated",
	})
	if err != nil {
		t.Fatal(err)
	}
	if owner.Unregister("", original) {
		t.Fatal("stale registration removed new default")
	}
	if found, _ := owner.Find("session-a", "compact"); found.ID != local.ID {
		t.Fatal("global update overwrote Session override")
	}
	if found, _ := owner.Find("session-b", "compact"); found.ID != updated.ID {
		t.Fatal("Session did not inherit current default")
	}
	if !owner.Unregister("session-a", local) {
		t.Fatal("override was not removed")
	}
	if found, _ := owner.Find("session-a", "compact"); found.ID != updated.ID {
		t.Fatal("withdrawal did not restore inheritance")
	}
	if _, err := owner.Register("", Definition{
		Name: "compact",
	}); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	if found, _ := owner.Find("session-a", "compact"); found.ID != updated.ID {
		t.Fatal("failed update modified default")
	}
}
