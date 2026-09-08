package deepseek_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gorenx/goren/credentials"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/deepseek"
	"strings"
	"sync/atomic"
	"testing"
)

type platformFixture struct {
	values    map[string]string
	home      string
	homeCalls *atomic.Int32
}

func (deployment platformFixture) Lookup(variableName string) (string, bool) {
	value, found := deployment.values[variableName]
	return value, found
}

func (deployment platformFixture) UserHomeDir() (string, error) {
	if deployment.homeCalls != nil {
		deployment.homeCalls.Add(1)
	}
	return deployment.home, nil
}

type credentialStoreFixture struct{}

func (credentialStoreFixture) Load(context.Context, string) (string, bool, error) {
	return "", false, nil
}

func (credentialStoreFixture) Save(context.Context, string, string) error {
	return nil
}

func (credentialStoreFixture) Delete(context.Context, string) error {
	return nil
}

func (credentialStoreFixture) Source() string {
	return "fixture"
}

func testCredentials(t *testing.T) credentials.Provider {
	t.Helper()
	business, err := credentials.NewManager(credentialStoreFixture{}, platformFixture{})
	if err != nil {
		t.Fatal(err)
	}
	return business
}

func TestConnectionResolvesCredentialsAndIdentityRemainsLazy(t *testing.T) {
	var calls atomic.Int32
	environment := platformFixture{
		home:      t.TempDir(),
		homeCalls: &calls,
	}
	source, err := deepseek.NewConnection(deepseek.Config{}, environment, testCredentials(t))
	if err != nil {
		t.Fatal(err)
	}
	installation, err := deepseek.NewIdentity(environment)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deepseek.NewAdapter(deepseek.AdapterDependencies{
		Connections: source,
		Credentials: source,
		Identity:    installation,
	}); err != nil {
		t.Fatal(err)
	}
	options, err := source.CurrentConnection()
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.ResolveAPIKey(context.Background(), options)
	var missing *llm.LlmError
	if !errors.As(err, &missing) || missing.Code() != "MISSING_CREDENTIAL" {
		t.Fatalf("credential error: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("startup resolved identity")
	}
	first, err := installation.UserID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := installation.UserID()
	if err != nil || first == "" || first != second || calls.Load() != 1 {
		t.Fatalf("identity reuse: %q %q %v", first, second, err)
	}
}
func TestConnectionConfigRejectsMalformedInput(t *testing.T) {
	for _, input := range []struct {
		raw     string
		message string
	}{
		{
			raw:     `{"unknown":true}`,
			message: "unknown field",
		},
		{
			raw:     `{"models":[{"id":"m","unknown":true}]}`,
			message: "unknown field",
		},
		{
			raw:     `null`,
			message: "must be an object",
		},
		{
			raw:     `{"baseURL":"https://one.example","baseURL":"https://two.example"}`,
			message: "duplicate field",
		},
	} {
		var settings deepseek.Config
		if err := json.Unmarshal([]byte(input.raw), &settings); err == nil || !strings.Contains(err.Error(), input.message) {
			t.Fatalf("decode %s: %v", input.raw, err)
		}
	}
}
