package anonymoususerid

import (
	"os"
	"path/filepath"
	"testing"
)

type environmentFixture struct {
	values map[string]string
	home   string
}

func (platform environmentFixture) Lookup(variableName string) (string, bool) {
	configuredValue, found := platform.values[variableName]
	return configuredValue, found
}

func (platform environmentFixture) UserHomeDir() (string, error) {
	return platform.home, nil
}

func TestStorePersistsPerHarnessHome(t *testing.T) {
	t.Parallel()
	harnessHome := t.TempDir()
	platform := environmentFixture{
		values: map[string]string{
			"DSH_HOME": harnessHome,
		},
	}
	firstStore, err := New(platform)
	if err != nil {
		t.Fatal(err)
	}
	firstValue, err := firstStore.Value()
	if err != nil {
		t.Fatal(err)
	}
	secondStore, err := New(platform)
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
	storage, err := New(environmentFixture{
		values: map[string]string{
			"DSH_HOME": harnessHome,
		},
	})
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
