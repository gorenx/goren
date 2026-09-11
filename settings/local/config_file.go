package local

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/gorenx/goren/mcpclient"
	configfile "github.com/gorenx/goren/settings/local/internal/file"
)

// LoadFile reads exactly the supplied YAML or JSON file. There is no implicit
// search, interpolation, watcher or runtime resource acquisition. Relative
// storage and MCP file paths are anchored to the supplied file's directory.
func LoadFile(path string) (Config, error) {
	if path == "" {
		return Config{}, ErrInvalid
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Config{}, ErrInvalid
	}
	var decoded Config
	if err := configfile.Read(absolute, &decoded); err != nil {
		return Config{}, err
	}
	if decoded.SchemaVersion == nil || *decoded.SchemaVersion != 1 {
		return Config{}, errors.New("settings: configuration requires schemaVersion 1")
	}
	if err := decoded.validate(); err != nil {
		return Config{}, err
	}
	decoded.resolvePaths(filepath.Dir(absolute))
	return decoded, nil
}

// Load combines file, whitelisted environment, and explicit CLI overrides
// in that order. Lookup is a stateless environment boundary; nil ignores it.
// It returns named inputs, leaving capability defaults to their owners.
func Load(path string, overrides Config, lookup func(string) (string, bool)) (Config, error) {
	var fromFile Config
	if path != "" {
		decoded, err := LoadFile(path)
		if err != nil {
			return Config{}, err
		}
		fromFile = decoded
	}
	fromEnvironment, err := localEnvironment(lookup)
	if err != nil {
		return Config{}, err
	}
	// Detach caller-owned slices and pointers before normalizing their paths.
	var fromCommand Config
	if err := configfile.Merge(&fromCommand, overrides); err != nil {
		return Config{}, err
	}
	if err := fromCommand.validate(); err != nil {
		return Config{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return Config{}, errors.New("settings: cannot resolve process directory")
	}
	fromEnvironment.resolvePaths(cwd)
	fromCommand.resolvePaths(cwd)
	var merged Config
	if err := configfile.Merge(&merged, fromFile, fromEnvironment, fromCommand); err != nil {
		return Config{}, err
	}
	if err := merged.validate(); err != nil {
		return Config{}, err
	}
	if err := merged.storageDefaults(); err != nil {
		return Config{}, err
	}
	return merged, nil
}

// Connections resolves inline and referenced MCP definitions without connecting.
// Duplicate names are rejected across both sources before any network effect.
func (input Config) Connections() ([]mcpclient.Config, error) {
	entries := []mcpclient.Config{}
	if input.MCP.Servers != nil {
		entries = append(entries, (*input.MCP.Servers)...)
	}
	if input.MCP.Files != nil {
		for _, path := range *input.MCP.Files {
			var decoded mcpclient.Config
			if err := configfile.Read(path, &decoded); err != nil {
				return nil, err
			}
			entries = append(entries, decoded)
		}
	}
	names := make(map[string]bool, len(entries))
	for index, entry := range entries {
		validated, err := entry.Validate()
		if err != nil || names[entry.ServerName] {
			return nil, errors.New("settings: invalid or duplicate MCP definition")
		}
		names[entry.ServerName] = true
		entries[index] = validated
	}
	return entries, nil
}
