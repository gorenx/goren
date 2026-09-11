package local

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/gorenx/goren/mcpclient"
	jsonparser "github.com/knadh/koanf/parsers/json"
	yamlparser "github.com/knadh/koanf/parsers/yaml"
	fileprovider "github.com/knadh/koanf/providers/file"
	structprovider "github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
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
	if err := readConfigFile(absolute, &decoded); err != nil {
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
// in that order. Environment values come from the current process.
// It returns named inputs, leaving capability defaults to their owners.
func Load(path string, overrides Config) (Config, error) {
	var fromFile Config
	if path != "" {
		decoded, err := LoadFile(path)
		if err != nil {
			return Config{}, err
		}
		fromFile = decoded
	}
	fromEnvironment, err := localEnvironment()
	if err != nil {
		return Config{}, err
	}
	fromCommand := overrides
	if err := fromCommand.validate(); err != nil {
		return Config{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return Config{}, errors.New("settings: cannot resolve process directory")
	}
	fromEnvironment.resolvePaths(cwd)
	fromCommand.resolvePaths(cwd)
	loaded := koanf.New(".")
	for _, source := range []Config{fromFile, fromEnvironment, fromCommand} {
		if err := loaded.Load(structprovider.Provider(source, "json"), nil); err != nil {
			return Config{}, ErrInvalid
		}
	}
	var merged Config
	if err := loaded.UnmarshalWithConf("", &merged, configDecodeOptions()); err != nil {
		return Config{}, ErrInvalid
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
			if err := readConfigFile(path, &decoded); err != nil {
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

func readConfigFile(path string, destination any) error {
	var format koanf.Parser
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		format = jsonparser.Parser()
	case "", ".yaml", ".yml":
		format = yamlparser.Parser()
	default:
		return ErrInvalid
	}
	loaded := koanf.New(".")
	if err := loaded.Load(fileprovider.Provider(path), format); err != nil {
		return ErrInvalid
	}
	if err := loaded.UnmarshalWithConf("", destination, configDecodeOptions()); err != nil {
		return ErrInvalid
	}
	return nil
}

// configDecodeOptions reuses owner JSON tags and rejects unknown fields without coercing strings.
func configDecodeOptions() koanf.UnmarshalConf {
	return koanf.UnmarshalConf{
		Tag: "json",
		DecoderConfig: &mapstructure.DecoderConfig{
			ErrorUnused:      true,
			ZeroFields:       true,
			WeaklyTypedInput: false,
			MatchName: func(key string, field string) bool {
				return key == field
			},
		},
	}
}
