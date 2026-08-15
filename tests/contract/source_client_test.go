//go:build contract

package contract_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/apiproxy"
	connectionhost "github.com/gorenx/goren/internal/connection"
)

type sourceClientObservation struct {
	Description struct {
		Result struct {
			OK    bool `json:"ok"`
			Value struct {
				Version          string `json:"version"`
				CWD              string `json:"cwd"`
				AttachedSessions int    `json:"attachedSessions"`
				CanOpenPath      bool   `json:"canOpenPath"`
			} `json:"value"`
		} `json:"result"`
	} `json:"description"`
	MuxOpened  bool `json:"muxOpened"`
	HostOpened bool `json:"hostOpened"`
	Mux        struct {
		RPCID   string `json:"rpcId"`
		Payload struct {
			Type string `json:"type"`
		} `json:"payload"`
	} `json:"mux"`
	Host struct {
		RPCID   string `json:"rpcId"`
		Payload struct {
			Type string `json:"type"`
		} `json:"payload"`
	} `json:"host"`
	Receipt struct {
		Accepted bool   `json:"accepted"`
		Reason   string `json:"reason"`
	} `json:"receipt"`
}

type reconnectObservation struct {
	ConnectedCount int      `json:"connectedCount"`
	MuxCount       int      `json:"muxCount"`
	HostCount      int      `json:"hostCount"`
	States         []string `json:"states"`
}

func TestPinnedSourceGeneratesCommittedVectors(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	commandContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := runTypeScript(commandContext, sourceRoot,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "generate-vectors.ts"),
		sourceRoot,
		filepath.Join(repositoryRoot, "contracts", "deepseek-harness", "manifest.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(repositoryRoot, "contracts", "deepseek-harness", "vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output, want) {
		t.Fatal("pinned source schemas no longer generate contracts/deepseek-harness/vectors.json")
	}
}

func TestPinnedSourceWebApiClientTalksToGoHost(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	methods := apiproxy.NewCatalog()
	provider := apiproxy.HostDescriptionFunc(func(context.Context) (apiproxy.HostDescription, error) {
		return apiproxy.HostDescription{
			Version: "0.1.0-rc.5", CWD: "/contract-workspace", AttachedSessions: 0, CanOpenPath: false,
		}, nil
	})
	if err := apiproxy.RegisterHostDescribe(methods, provider); err != nil {
		t.Fatal(err)
	}
	muxHandler := func(requestContext context.Context, emit func(apiproxy.StreamRequest[apiproxy.MuxFrame]) error) error {
		if err := emit(apiproxy.StreamRequest[apiproxy.MuxFrame]{
			RPCID: "mux-rpc", Payload: apiproxy.SessionSubscribedFrame{SessionID: "session-1", LastSeq: -1},
		}); err != nil {
			return err
		}
		<-requestContext.Done()
		return nil
	}
	hostHandler := func(requestContext context.Context, emit func(apiproxy.StreamRequest[apiproxy.HostFrame]) error) error {
		if err := emit(apiproxy.StreamRequest[apiproxy.HostFrame]{
			RPCID: "host-rpc", Payload: apiproxy.HostSessionAddedFrame{SessionID: "session-1", Blank: true},
		}); err != nil {
			return err
		}
		<-requestContext.Done()
		return nil
	}
	streams, err := apiproxy.NewEventStreams(muxHandler, hostHandler)
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
		closeContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := httpHost.Close(closeContext); err != nil {
			t.Errorf("close Go host: %v", err)
		}
	}()

	commandContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := runTypeScript(commandContext, sourceRoot,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "client-smoke.ts"), sourceRoot, testServer.URL,
	)
	if err != nil {
		t.Fatal(err)
	}
	var observation sourceClientObservation
	if err := json.Unmarshal(output, &observation); err != nil {
		t.Fatalf("decode source client observation: %v; output = %s", err, output)
	}
	if !observation.Description.Result.OK || observation.Description.Result.Value.Version != "0.1.0-rc.5" ||
		observation.Description.Result.Value.CWD != "/contract-workspace" {
		t.Fatalf("host.describe observation = %#v", observation.Description)
	}
	if !observation.MuxOpened || !observation.HostOpened || observation.Mux.RPCID != "mux-rpc" ||
		observation.Mux.Payload.Type != "session/subscribed" || observation.Host.RPCID != "host-rpc" ||
		observation.Host.Payload.Type != "host/session-added" {
		t.Fatalf("stream observation = mux %#v, host %#v", observation.Mux, observation.Host)
	}
	if observation.Receipt.Accepted || observation.Receipt.Reason != "not-pending" {
		t.Fatalf("respond receipt = %#v", observation.Receipt)
	}
}

func TestPinnedSourceConnectionRebuildsBothStreams(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	methods := apiproxy.NewCatalog()
	provider := apiproxy.HostDescriptionFunc(func(context.Context) (apiproxy.HostDescription, error) {
		return apiproxy.HostDescription{
			Version: "0.1.0-rc.5", CWD: "/contract-workspace", AttachedSessions: 0, CanOpenPath: false,
		}, nil
	})
	if err := apiproxy.RegisterHostDescribe(methods, provider); err != nil {
		t.Fatal(err)
	}
	firstMuxRelease := make(chan struct{})
	var releaseOnce sync.Once
	if err := apiproxy.RegisterUnary(methods, "contract.release", apiproxy.DecodeObject[struct{}],
		func(context.Context, apiproxy.Request[struct{}]) (apiproxy.Outcome[struct{}], error) {
			releaseOnce.Do(func() { close(firstMuxRelease) })
			return apiproxy.OK(struct{}{}), nil
		}); err != nil {
		t.Fatal(err)
	}

	var muxOpenCount atomic.Int32
	var hostOpenCount atomic.Int32
	muxHandler := func(requestContext context.Context, emit func(apiproxy.StreamRequest[apiproxy.MuxFrame]) error) error {
		generation := muxOpenCount.Add(1)
		if err := emit(apiproxy.StreamRequest[apiproxy.MuxFrame]{
			RPCID: "mux-generation", Payload: apiproxy.SessionSubscribedFrame{SessionID: "session-1", LastSeq: -1},
		}); err != nil {
			return err
		}
		if generation == 1 {
			select {
			case <-requestContext.Done():
				return nil
			case <-firstMuxRelease:
				return nil
			}
		}
		<-requestContext.Done()
		return nil
	}
	hostHandler := func(requestContext context.Context, emit func(apiproxy.StreamRequest[apiproxy.HostFrame]) error) error {
		hostOpenCount.Add(1)
		if err := emit(apiproxy.StreamRequest[apiproxy.HostFrame]{
			RPCID: "host-generation", Payload: apiproxy.HostSessionStatusFrame{SessionID: "session-1", Running: true},
		}); err != nil {
			return err
		}
		<-requestContext.Done()
		return nil
	}
	streams, err := apiproxy.NewEventStreams(muxHandler, hostHandler)
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
		closeContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := httpHost.Close(closeContext); err != nil {
			t.Errorf("close Go host: %v", err)
		}
	}()

	commandContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := runTypeScript(commandContext, sourceRoot,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "connection-reconnect.ts"), sourceRoot, testServer.URL,
	)
	if err != nil {
		t.Fatal(err)
	}
	var observation reconnectObservation
	if err := json.Unmarshal(output, &observation); err != nil {
		t.Fatalf("decode reconnect observation: %v; output = %s", err, output)
	}
	if observation.ConnectedCount != 2 || observation.MuxCount < 2 || observation.HostCount < 2 {
		t.Fatalf("source Connection observation = %#v", observation)
	}
	if muxOpenCount.Load() < 2 || hostOpenCount.Load() < 2 {
		t.Fatalf("Go stream opens = mux %d, host %d", muxOpenCount.Load(), hostOpenCount.Load())
	}
	wantStates := []string{"connected", "reconnecting", "connected"}
	if !slices.Equal(observation.States, wantStates) {
		t.Fatalf("states = %v, want %v", observation.States, wantStates)
	}
}

func contractPaths(t *testing.T) (string, string) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve contract test path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	sourceRoot := os.Getenv("DSH_SOURCE")
	if sourceRoot == "" {
		sourceRoot = filepath.Join(repositoryRoot, "..", "deepseek-harness")
	}
	return repositoryRoot, filepath.Clean(sourceRoot)
}

func runTypeScript(requestContext context.Context, sourceRoot string, arguments ...string) ([]byte, error) {
	return runTypeScriptInput(requestContext, sourceRoot, nil, arguments...)
}

func runTypeScriptInput(requestContext context.Context, sourceRoot string, input []byte, arguments ...string) ([]byte, error) {
	tsxPath := filepath.Join(sourceRoot, "node_modules", ".bin", "tsx")
	if _, err := os.Stat(tsxPath); err != nil {
		return nil, errors.New("source TypeScript dependencies are unavailable; run corepack pnpm install --frozen-lockfile in DSH_SOURCE")
	}
	nodePath, err := exec.LookPath("node")
	if err != nil {
		return nil, errors.New("Node.js is unavailable")
	}
	commandArguments := append([]string{"--import", "tsx"}, arguments...)
	// Run the TS loader in the Node process owned by CommandContext. Invoking
	// the tsx launcher would add a child that can outlive a timed-out test and
	// retain WebSocket and output-pipe file descriptors.
	command := exec.CommandContext(requestContext, nodePath, commandArguments...)
	command.Dir = sourceRoot
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	output, err := command.Output()
	if err == nil {
		return output, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return nil, errors.New(string(exitError.Stderr))
	}
	return nil, err
}
