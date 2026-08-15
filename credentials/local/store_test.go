package local_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gorenx/goren/credentials"
	credentialslocal "github.com/gorenx/goren/credentials/local"
)

func TestManagerPersistsAndRemovesCredentialWithoutExposingItInDescription(t *testing.T) {
	t.Parallel()
	documentPath := filepath.Join(t.TempDir(), ".credentials.json")
	storage, err := credentialslocal.NewStore(credentialslocal.Config{Path: documentPath})
	if err != nil {
		t.Fatal(err)
	}
	credentialManager, err := credentials.NewManager(storage, credentials.Environment{
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := credentials.NewRef("DEEPSEEK_API_KEY")
	if err != nil {
		t.Fatal(err)
	}
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
	resolved, found, err := credentialManager.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if !found || resolved.Value != testValue || resolved.Source != "file" {
		t.Fatalf("resolved = (%#v, %t)", resolved, found)
	}
	fileInfo, err := os.Stat(documentPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("document mode = %04o", fileInfo.Mode().Perm())
	}
	reloadedStorage, err := credentialslocal.NewStore(credentialslocal.Config{Path: documentPath})
	if err != nil {
		t.Fatal(err)
	}
	reloadedManager, err := credentials.NewManager(reloadedStorage, credentials.Environment{
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatal(err)
	}
	if reloaded, reloadedFound, resolveErr := reloadedManager.Resolve(context.Background(), ref); resolveErr != nil || !reloadedFound || reloaded.Value != testValue {
		t.Fatalf("reloaded resolve = (%#v, %t, %v)", reloaded, reloadedFound, resolveErr)
	}
	if err := reloadedManager.Unset(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if _, remaining, err := reloadedManager.Resolve(context.Background(), ref); err != nil || remaining {
		t.Fatalf("resolve after unset = (%t, %v)", remaining, err)
	}
}

func TestManagerTreatsLaunchingEnvironmentAsReadOnlyWinningLayer(t *testing.T) {
	t.Parallel()
	documentPath := filepath.Join(t.TempDir(), ".credentials.json")
	storage, err := credentialslocal.NewStore(credentialslocal.Config{Path: documentPath})
	if err != nil {
		t.Fatal(err)
	}
	credentialManager, err := credentials.NewManager(storage, credentials.Environment{
		LookupEnv: func(name string) (string, bool) {
			if name == "DEEPSEEK_API_KEY" {
				return "environment-test-value", true
			}
			return "", false
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := credentials.NewRef("DEEPSEEK_API_KEY")
	if err != nil {
		t.Fatal(err)
	}
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
