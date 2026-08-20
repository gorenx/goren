// Package local provides an owner-only file-backed Credentials storage adapter.
package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/gorenx/goren/credentials"
)

const groupOtherPermissions os.FileMode = 0o077

// Config is the strict adapter-owned deployment configuration.
type Config struct {
	Path string `json:"path"`
}

// LiveStore stores credential values in one owner-only JSON document. It owns
// only file I/O; precedence and writability rules belong to credentials.Manager.
type LiveStore struct {
	path string
	mu   sync.Mutex
}

// ValidateConfig checks the adapter-owned document location.
func ValidateConfig(settings Config) error {
	if settings.Path == "" || !filepath.IsAbs(settings.Path) {
		return errors.New("credentials local: path must be absolute")
	}
	return nil
}

// NewLiveStore constructs the adapter and validates any existing document.
func NewLiveStore(settings Config) (*LiveStore, error) {
	if err := ValidateConfig(settings); err != nil {
		return nil, err
	}
	storage := &LiveStore{path: filepath.Clean(settings.Path)}
	if _, err := storage.read(); err != nil {
		return nil, err
	}
	return storage, nil
}

// Source returns the stable label used in value-free credential metadata.
func (*LiveStore) Source() string { return "file" }

// Load reads one stored value from the latest committed document.
func (storage *LiveStore) Load(requestContext context.Context, ref credentials.Ref) (string, bool, error) {
	if err := requestContext.Err(); err != nil {
		return "", false, err
	}
	storage.mu.Lock()
	defer storage.mu.Unlock()
	values, err := storage.read()
	if err != nil {
		return "", false, err
	}
	value, found := values[ref]
	return value, found && value != "", nil
}

// Save atomically commits one non-empty value.
func (storage *LiveStore) Save(requestContext context.Context, ref credentials.Ref, value string) error {
	if value == "" {
		return errors.New("credentials local: storage value must be non-empty")
	}
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if err := requestContext.Err(); err != nil {
		return err
	}
	values, err := storage.read()
	if err != nil {
		return err
	}
	values[ref] = value
	return storage.write(values)
}

// Delete atomically removes one value and is idempotent when absent.
func (storage *LiveStore) Delete(requestContext context.Context, ref credentials.Ref) error {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if err := requestContext.Err(); err != nil {
		return err
	}
	values, err := storage.read()
	if err != nil {
		return err
	}
	if _, found := values[ref]; !found {
		return nil
	}
	delete(values, ref)
	return storage.write(values)
}

// Path returns the managed document path for startup diagnostics.
func (storage *LiveStore) Path() string { return storage.path }

func (storage *LiveStore) read() (map[credentials.Ref]string, error) {
	fileInfo, statErr := os.Stat(storage.path)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("credentials local: inspect document: %w", statErr)
	}
	if statErr == nil && runtime.GOOS != "windows" && fileInfo.Mode().Perm()&groupOtherPermissions != 0 {
		return nil, fmt.Errorf("credentials local: document permissions must be 0600, got %04o", fileInfo.Mode().Perm())
	}
	encoded, readErr := os.ReadFile(storage.path)
	if errors.Is(readErr, os.ErrNotExist) {
		return make(map[credentials.Ref]string), nil
	}
	if readErr != nil {
		return nil, fmt.Errorf("credentials local: read document: %w", readErr)
	}
	return decodeDocument(encoded)
}

func (storage *LiveStore) write(values map[credentials.Ref]string) error {
	directory := filepath.Dir(storage.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("credentials local: create directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil && runtime.GOOS != "windows" {
		return fmt.Errorf("credentials local: protect directory: %w", err)
	}
	encoded, err := encodeDocument(values)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("credentials local: create temporary document: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("credentials local: protect temporary document: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("credentials local: write temporary document: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("credentials local: sync temporary document: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("credentials local: close temporary document: %w", err)
	}
	if err := os.Rename(temporaryPath, storage.path); err != nil {
		return fmt.Errorf("credentials local: commit document: %w", err)
	}
	committed = true
	return nil
}

func decodeDocument(encoded []byte) (map[credentials.Ref]string, error) {
	var document map[string]string
	if err := json.Unmarshal(encoded, &document); err != nil || document == nil {
		return nil, errors.New("credentials local: invalid JSON credential mapping")
	}
	values := make(map[credentials.Ref]string)
	for rawRef, value := range document {
		ref, err := credentials.NewRef(rawRef)
		if err != nil {
			return nil, errors.New("credentials local: document contains an invalid credential reference")
		}
		if value == "" {
			return nil, fmt.Errorf("credentials local: value for %q must be a non-empty string", ref)
		}
		values[ref] = value
	}
	return values, nil
}

func encodeDocument(values map[credentials.Ref]string) ([]byte, error) {
	document := make(map[string]string, len(values))
	for ref, value := range values {
		document[string(ref)] = value
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, errors.New("credentials local: encode document")
	}
	return append(encoded, '\n'), nil
}
