package agent_test

import (
	"errors"
	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"testing"
)

func TestRegistryOwnsDetachedParentRelations(t *testing.T) {
	directory := &agentcore.Registry{}
	parent := session.SessionID("parent")
	if err := directory.Register(agentcore.Registration{
		ID: parent,
	}); err != nil {
		t.Fatal(err)
	}
	for _, identifier := range []session.SessionID{"child-b", "child-a"} {
		if err := directory.Register(agentcore.Registration{
			ID:       identifier,
			ParentID: &parent,
		}); err != nil {
			t.Fatal(err)
		}
	}
	parent = "mutated"
	entry, found := directory.Get("child-a")
	if !found || *entry.ParentID != "parent" {
		t.Fatal("registration retained caller pointer")
	}
	*entry.ParentID = "changed"
	children := directory.Children("parent")
	if len(children) != 2 || children[0] != "child-a" || children[1] != "child-b" {
		t.Fatalf("children = %v", children)
	}
	if err := directory.Remove("parent"); err == nil {
		t.Fatal("removed parent before children")
	}
	listed := directory.List()
	if len(listed) != 3 || listed[0].ID != "child-a" {
		t.Fatalf("list = %#v", listed)
	}
	*listed[0].ParentID = "changed list"
	if len(directory.Children("parent")) != 2 {
		t.Fatal("list exposed retained relation")
	}
	for _, identifier := range []session.SessionID{"child-a", "child-b", "parent", "parent"} {
		if err := directory.Remove(identifier); err != nil {
			t.Fatal(err)
		}
	}
	if len(directory.List()) != 0 {
		t.Fatal("removed registrations remain")
	}
}

func TestRegistryRejectsInvalidRelationsWithoutMutation(t *testing.T) {
	directory := &agentcore.Registry{}
	self := session.SessionID("self")
	for _, entry := range []agentcore.Registration{
		{},
		{
			ID:       self,
			ParentID: &self,
		},
		{
			ID:       "child",
			ParentID: &self,
		},
	} {
		if err := directory.Register(entry); err == nil {
			t.Fatalf("accepted %#v", entry)
		}
	}
	if len(directory.List()) != 0 {
		t.Fatal("failed registration changed directory")
	}
	if err := directory.Register(agentcore.Registration{
		ID: self,
	}); err != nil {
		t.Fatal(err)
	}
	if err := directory.Register(agentcore.Registration{
		ID: self,
	}); err == nil {
		t.Fatal("duplicate registration succeeded")
	}
}

func TestRegistryClosingAncestorRejectsNewChildrenButRetainsAcceptedWork(t *testing.T) {
	directory := &agentcore.Registry{}
	root := session.SessionID("root")
	child := session.SessionID("child")
	grandchild := session.SessionID("grandchild")
	for _, entry := range []agentcore.Registration{
		{
			ID: root,
		},
		{
			ID:       child,
			ParentID: &root,
		},
		{
			ID:       grandchild,
			ParentID: &child,
		},
	} {
		if err := directory.Register(entry); err != nil {
			t.Fatal(err)
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := directory.BeginClose(root); err != nil {
			t.Fatal(err)
		}
	}
	for _, identifier := range []session.SessionID{root, child, grandchild} {
		if !errors.Is(directory.CheckAdmission(identifier), agentcore.ErrClosing) {
			t.Fatalf("admitted closing descendant %q", identifier)
		}
		if err := directory.Register(agentcore.Registration{
			ID:       "new-" + identifier,
			ParentID: &identifier,
		}); !errors.Is(err, agentcore.ErrClosing) {
			t.Fatalf("creation under %q = %v", identifier, err)
		}
	}
	if len(directory.List()) != 3 || len(directory.Children(child)) != 1 {
		t.Fatal("closing erased accepted descendants")
	}
	if err := directory.Register(agentcore.Registration{
		ID: "independent",
	}); err != nil {
		t.Fatal(err)
	}
	if err := directory.CheckAdmission("independent"); err != nil {
		t.Fatal(err)
	}
	if err := directory.Remove(root); err == nil {
		t.Fatal("closing parent removed before children")
	}
	for _, identifier := range []session.SessionID{grandchild, child, root} {
		if err := directory.Remove(identifier); err != nil {
			t.Fatal(err)
		}
	}
	if err := directory.Register(agentcore.Registration{
		ID: root,
	}); err != nil {
		t.Fatal(err)
	}
	if err := directory.CheckAdmission(root); err != nil {
		t.Fatal("new registration inherited old close", err)
	}
}

func TestRegistryUnknownCloseDoesNotBlockFutureRegistration(t *testing.T) {
	directory := &agentcore.Registry{}
	if err := directory.BeginClose("future"); err == nil {
		t.Fatal("unknown close succeeded")
	}
	if err := directory.CheckAdmission("future"); err == nil {
		t.Fatal("unknown identity admitted")
	}
	if err := directory.Register(agentcore.Registration{
		ID: "future",
	}); err != nil {
		t.Fatal(err)
	}
	if err := directory.CheckAdmission("future"); err != nil {
		t.Fatal(err)
	}
}
