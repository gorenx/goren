package registry

import "testing"

func TestRegistryBindsOneInstanceAndProtectsReplacement(t *testing.T) {
	var directory authority
	if err := directory.Claim("agent", "old", "instance"); err == nil {
		t.Fatal("unreserved creation authorized")
	}
	if err := directory.Reserve("agent", "old"); err != nil {
		t.Fatal(err)
	}
	if err := directory.Reserve("agent", "old"); err != nil {
		t.Fatal(err)
	}
	if err := directory.Reserve("agent", "new"); err == nil {
		t.Fatal("live reservation replaced")
	}
	if err := directory.Claim("agent", "old", "first"); err != nil {
		t.Fatal(err)
	}
	if err := directory.Claim("agent", "old", "first"); err != nil {
		t.Fatal(err)
	}
	if err := directory.Claim("agent", "old", "reactivated"); err == nil {
		t.Fatal("reactivated owner inherited authority")
	}
	if err := directory.Release("agent", "old"); err == nil {
		t.Fatal("live reservation released")
	}
	if err := directory.Retire("agent", "old"); err != nil {
		t.Fatal(err)
	}
	if err := directory.Retire("agent", "old"); err != nil {
		t.Fatal(err)
	}
	if err := directory.Reserve("agent", "old"); err == nil {
		t.Fatal("retired creation revived")
	}
	if err := directory.Claim("agent", "old", "first"); err == nil {
		t.Fatal("retired owner authorized")
	}
	if err := directory.Release("agent", "old"); err != nil {
		t.Fatal(err)
	}
	if err := directory.Release("agent", "old"); err != nil {
		t.Fatal(err)
	}
	if err := directory.Reserve("agent", "new"); err != nil {
		t.Fatal(err)
	}
	if err := directory.Retire("agent", "old"); err == nil {
		t.Fatal("old retirement affected replacement")
	}
	if err := directory.Release("agent", "old"); err == nil {
		t.Fatal("old release removed replacement")
	}
	if err := directory.Claim("agent", "old", "first"); err == nil {
		t.Fatal("old creation claimed replacement")
	}
	if err := directory.Claim("agent", "new", "second"); err != nil {
		t.Fatal(err)
	}
}
