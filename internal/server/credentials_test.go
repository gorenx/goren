package server

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/gorenx/goren/credentials"
	credentialslocal "github.com/gorenx/goren/credentials/local"
	"github.com/gorenx/goren/systemprompt"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type emptyEnvironment struct{}

func (emptyEnvironment) Lookup(string) (string, bool) { return "", false }

func TestSharedCredentialsAndPromptInitialization(t *testing.T) {
	ctx := context.Background()
	system, err := goakt.NewActorSystem("server-resources", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := system.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	secrets, err := initializeCredentials(ctx, system, credentialslocal.Config{
		Path: filepath.Join(t.TempDir(), "credentials.json"),
	}, emptyEnvironment{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = secrets.Resolve(ctx, "DEEPSEEK_API_KEY")
	if !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("absent credential: %v", err)
	}
	// Prompt initialization is independent of the presence of a provider secret.
	if _, err := initializePrompts(ctx, system, systemprompt.Config{}); err != nil {
		t.Fatal(err)
	}
	if _, err := initializePrompts(ctx, system, systemprompt.Config{}); err == nil {
		t.Fatal("duplicate Prompt initialization accepted")
	}
}
