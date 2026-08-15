package anonymoususerid

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStorePersistsPerHarnessHome(t *testing.T) {
	t.Parallel()
	harnessHome := t.TempDir()
	lookupEnv := func(name string) (string, bool) {
		if name == "DSH_HOME" {
			return harnessHome, true
		}
		return "", false
	}
	firstStore, err := New(lookupEnv, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstValue, err := firstStore.Value()
	if err != nil {
		t.Fatal(err)
	}
	secondStore, err := New(lookupEnv, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondValue, err := secondStore.Value()
	if err != nil {
		t.Fatal(err)
	}
	if firstValue == "" || secondValue != firstValue {
		t.Fatalf("ids = (%q, %q)", firstValue, secondValue)
	}
	content, err := os.ReadFile(filepath.Join(harnessHome, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != firstValue+"\n" {
		t.Fatalf("persisted id = %q", content)
	}
}

func TestStoreReplacesCorruptIdentity(t *testing.T) {
	t.Parallel()
	harnessHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(harnessHome, FileName), []byte("corrupt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	storage, err := New(func(string) (string, bool) { return harnessHome, true }, nil)
	if err != nil {
		t.Fatal(err)
	}
	identifier, err := storage.Value()
	if err != nil {
		t.Fatal(err)
	}
	if !uuidPattern.MatchString(identifier) {
		t.Fatalf("replacement id = %q", identifier)
	}
}
