//go:build contract

package fixture

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// Paths resolves the Go repository and fixed TypeScript source roots used by
// cross-language contract tests.
func Paths(testingContext testing.TB) (string, string) {
	testingContext.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		testingContext.Fatal("resolve contract fixture path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", ".."))
	sourceRoot := os.Getenv("DSH_SOURCE")
	if sourceRoot == "" {
		sourceRoot = filepath.Join(repositoryRoot, "..", "deepseek-harness")
	}
	return repositoryRoot, filepath.Clean(sourceRoot)
}

// RunTypeScript runs one fixed-source TypeScript oracle in the Node process
// owned by requestContext, so timeout cancellation cannot leave a launcher
// child holding WebSocket or output descriptors.
func RunTypeScript(requestContext context.Context, sourceRoot string, input []byte, arguments ...string) ([]byte, error) {
	tsxPath := filepath.Join(sourceRoot, "node_modules", ".bin", "tsx")
	if _, err := os.Stat(tsxPath); err != nil {
		return nil, errors.New("source TypeScript dependencies are unavailable; run corepack pnpm install --frozen-lockfile in DSH_SOURCE")
	}
	nodePath, err := exec.LookPath("node")
	if err != nil {
		return nil, errors.New("Node.js is unavailable")
	}
	commandArguments := append([]string{"--import", "tsx"}, arguments...)
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
