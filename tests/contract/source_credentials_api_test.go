//go:build contract

package contract_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/credentials"
	credentialslocal "github.com/gorenx/goren/credentials/local"
	connectionhost "github.com/gorenx/goren/internal/connection"
)

type contractEnvironment struct{}

func (contractEnvironment) Lookup(string) (string, bool) {
	return "", false
}

func TestPinnedSourceCredentialsWebApiClientUsesGoProvider(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	storage, err := credentialslocal.NewLiveStore(
		credentialslocal.Config{Path: filepath.Join(t.TempDir(), ".credentials.json")},
	)
	if err != nil {
		t.Fatal(err)
	}
	credentialManager, err := credentials.NewManager(storage, contractEnvironment{})
	if err != nil {
		t.Fatal(err)
	}
	methods := apiproxy.NewCatalog()
	if err := apiproxy.RegisterCredentialsAPI(methods, apiproxy.NewCredentialsGateway(credentialManager)); err != nil {
		t.Fatal(err)
	}
	idleMux := func(requestContext context.Context, _ func(apiproxy.StreamRequest[apiproxy.MuxFrame]) error) error {
		<-requestContext.Done()
		return nil
	}
	idleHost := func(requestContext context.Context, _ func(apiproxy.StreamRequest[apiproxy.HostFrame]) error) error {
		<-requestContext.Done()
		return nil
	}
	streams, err := apiproxy.NewEventStreams(idleMux, idleHost)
	if err != nil {
		t.Fatal(err)
	}
	httpHost, err := connectionhost.NewHTTPHost(connectionhost.HTTPConfig{}, methods, streams)
	if err != nil {
		t.Fatal(err)
	}
	testServer := httptest.NewServer(httpHost)
	defer testServer.Close()
	defer func() {
		closeContext, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelClose()
		if err := httpHost.Close(closeContext); err != nil {
			t.Errorf("close Go host: %v", err)
		}
	}()

	commandContext, cancelCommand := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelCommand()
	output, err := runTypeScript(commandContext, sourceRoot,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "credentials-client.ts"),
		sourceRoot, testServer.URL,
	)
	if err != nil {
		t.Fatal(err)
	}
	var observation struct {
		Configured struct {
			Result struct {
				OK    bool `json:"ok"`
				Value struct {
					Credentials map[string]apiproxy.CredentialView `json:"credentials"`
				} `json:"value"`
			} `json:"result"`
		} `json:"configured"`
		Removed struct {
			Result struct {
				OK    bool `json:"ok"`
				Value struct {
					Credentials map[string]apiproxy.CredentialView `json:"credentials"`
				} `json:"value"`
			} `json:"result"`
		} `json:"removed"`
	}
	if err := json.Unmarshal(output, &observation); err != nil {
		t.Fatalf("decode source Credentials observation: %v; output = %s", err, output)
	}
	configured := observation.Configured.Result.Value.Credentials["DEEPSEEK_API_KEY"]
	removed := observation.Removed.Result.Value.Credentials["DEEPSEEK_API_KEY"]
	if !observation.Configured.Result.OK || !configured.Configured || configured.Source != "file" || !configured.Writable {
		t.Fatalf("configured response = %#v", observation.Configured)
	}
	if !observation.Removed.Result.OK || removed.Configured || !removed.Writable {
		t.Fatalf("removed response = %#v", observation.Removed)
	}
}
