package local_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gorenx/goren/credentials"
	credentialslocal "github.com/gorenx/goren/credentials/local"
)

type environmentFixture struct {
	values map[string]string
}

func (environment environmentFixture) Lookup(name string) (string, bool) {
	value, found := environment.values[name]
	return value, found
}

func TestManagerPersistsAndRemovesCredentialWithoutExposingItInDescription(t *testing.T) {
	t.Parallel()
	documentPath := filepath.Join(t.TempDir(), ".credentials.json")
	storage, err := credentialslocal.NewLiveStore(credentialslocal.Config{
		Path: documentPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	credentialManager, err := credentials.NewManager(storage, environmentFixture{
		values: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := "DEEPSEEK_API_KEY"
	const testValue = "test-credential-value"
	if err := credentialManager.Set(context.Background(), ref, testValue); err != nil {
		t.Fatal(err)
	}
	description, err := credentialManager.Describe(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if !description.Configured || description.Source != "file" || !description.Writable {
		t.Fatalf("description = %#v", description)
	}
	resolved, err := credentialManager.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Value != testValue || resolved.Source != "file" {
		t.Fatal("resolved credential differs")
	}
	fileInfo, err := os.Stat(documentPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("document mode = %04o", fileInfo.Mode().Perm())
	}
	reloadedStorage, err := credentialslocal.NewLiveStore(credentialslocal.Config{
		Path: documentPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	reloadedManager, err := credentials.NewManager(reloadedStorage, environmentFixture{
		values: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reloaded, resolveErr := reloadedManager.Resolve(context.Background(), ref); resolveErr != nil || reloaded.Value != testValue {
		t.Fatalf("reloaded resolve failed: %v", resolveErr)
	}
	if err := reloadedManager.Unset(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err := reloadedManager.Resolve(context.Background(), ref); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("resolve after unset: %v", err)
	}
}

func TestManagerTreatsLaunchingEnvironmentAsReadOnlyWinningLayer(t *testing.T) {
	t.Parallel()
	documentPath := filepath.Join(t.TempDir(), ".credentials.json")
	storage, err := credentialslocal.NewLiveStore(credentialslocal.Config{
		Path: documentPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	credentialManager, err := credentials.NewManager(storage, environmentFixture{
		values: map[string]string{
			"DEEPSEEK_API_KEY": "environment-test-value",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := "DEEPSEEK_API_KEY"
	description, err := credentialManager.Describe(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if !description.Configured || description.Source != "env" || description.Writable {
		t.Fatalf("description = %#v", description)
	}
	if err := credentialManager.Set(context.Background(), ref, "replacement-test-value"); err == nil || !strings.Contains(err.Error(), "launching environment") {
		t.Fatalf("set error = %v", err)
	}
	if _, err := os.Stat(documentPath); !os.IsNotExist(err) {
		t.Fatalf("shadowed write created document: %v", err)
	}
}
