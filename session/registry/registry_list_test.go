package registry

import (
	"github.com/gorenx/goren/session"
	"testing"
)

func TestListSessionsExcludesUninitializedAndRetiredCreations(t *testing.T) {
	owner := &RegistryGrain{}
	for _, identifier := range []session.SessionID{"b", "a", "pending"} {
		if err := owner.reserve(identifier, string(identifier)+"-creation"); err != nil {
			t.Fatal(err)
		}
	}
	cwd := "/original"
	for _, identifier := range []session.SessionID{"b", "a"} {
		owner.sessions[identifier].header = &session.Header{
			ID:  identifier,
			CWD: &cwd,
		}
	}
	listed := owner.listSessions()
	if len(listed.Items) != 2 || listed.Items[0].Header.ID != "a" || listed.Items[1].Header.ID != "b" {
		t.Fatalf("list = %#v", listed)
	}
	*listed.Items[0].Header.CWD = "/changed"
	if *owner.sessions["a"].header.CWD != "/original" {
		t.Fatal("list exposed retained metadata")
	}
	if err := owner.directory.Retire("a", "a-creation"); err != nil {
		t.Fatal(err)
	}
	listed = owner.listSessions()
	if len(listed.Items) != 1 || listed.Items[0].CreationID != "b-creation" {
		t.Fatalf("retired entry remained: %#v", listed)
	}
	if err := owner.directory.Release("a", "a-creation"); err != nil {
		t.Fatal(err)
	}
	if err := owner.reserve("a", "new-creation"); err != nil {
		t.Fatal(err)
	}
	listed = owner.listSessions()
	if len(listed.Items) != 1 {
		t.Fatal("new creation reused old initialized metadata")
	}
}
