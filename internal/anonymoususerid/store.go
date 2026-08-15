// Package anonymoususerid owns the harness-home-scoped random identity sent
// as model-hidden provider transport metadata.
package anonymoususerid

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const FileName = ".anonymous-user-id"

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type Store struct {
	filePath string

	mu    sync.Mutex
	value string
}

// New resolves the identity file from DSH_HOME, falling back to ~/.dsh.
func New(lookupEnv func(string) (string, bool), userHome func() (string, error)) (*Store, error) {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	if userHome == nil {
		userHome = os.UserHomeDir
	}
	harnessHome := ""
	if configuredHome, found := lookupEnv("DSH_HOME"); found && configuredHome != "" {
		harnessHome = configuredHome
	} else {
		resolvedHome, err := userHome()
		if err != nil {
			return nil, err
		}
		harnessHome = filepath.Join(resolvedHome, ".dsh")
	}
	return &Store{filePath: filepath.Join(harnessHome, FileName)}, nil
}

// Value returns a process-stable UUID, persisting it on a best-effort basis.
func (storage *Store) Value() (string, error) {
	if storage == nil || storage.filePath == "" {
		return "", errors.New("anonymous user id: store is not initialized")
	}
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if storage.value != "" {
		return storage.value, nil
	}
	if persisted := readPersisted(storage.filePath); persisted != "" {
		storage.value = persisted
		return persisted, nil
	}
	created, err := generateUUID()
	if err != nil {
		return "", err
	}
	_ = os.MkdirAll(filepath.Dir(storage.filePath), 0o700)
	fileHandle, createErr := os.OpenFile(storage.filePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if createErr == nil {
		_, writeErr := fileHandle.WriteString(created + "\n")
		closeErr := fileHandle.Close()
		if writeErr == nil && closeErr == nil {
			storage.value = created
			return created, nil
		}
	}
	if persisted := readPersisted(storage.filePath); persisted != "" {
		storage.value = persisted
		return persisted, nil
	}
	_ = os.WriteFile(storage.filePath, []byte(created+"\n"), 0o600)
	storage.value = created
	return created, nil
}

func readPersisted(filePath string) string {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return ""
	}
	candidate := strings.TrimSpace(string(content))
	if !uuidPattern.MatchString(candidate) {
		return ""
	}
	return candidate
}

func generateUUID() (string, error) {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", err
	}
	randomBytes[6] = randomBytes[6]&0x0f | 0x40
	randomBytes[8] = randomBytes[8]&0x3f | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], randomBytes[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], randomBytes[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], randomBytes[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], randomBytes[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], randomBytes[10:16])
	return string(encoded), nil
}
