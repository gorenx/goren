package factory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agentloop"
)

// Config is the strict Agent Loop plugin configuration owned by this Factory.
type Config struct {
	MaxParallelToolCalls *int
}

func (settings *Config) UnmarshalJSON(encoded []byte) error {
	if settings == nil {
		return errors.New("agentloop factory: cannot decode into nil Config")
	}
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return errors.New("agentloop factory: configuration must be an object")
	}
	var wireValue struct {
		MaxParallelToolCalls json.RawMessage `json:"maxParallelToolCalls"`
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wireValue); err != nil {
		return err
	}
	var decoded Config
	if len(wireValue.MaxParallelToolCalls) != 0 {
		if isNull(wireValue.MaxParallelToolCalls) {
			return errors.New(
				"agentloop factory: maxParallelToolCalls must be a positive integer",
			)
		}
		var parallelLimit int
		if err := json.Unmarshal(
			wireValue.MaxParallelToolCalls,
			&parallelLimit,
		); err != nil {
			return fmt.Errorf(
				"agentloop factory: maxParallelToolCalls must be a positive integer: %w",
				err,
			)
		}
		decoded.MaxParallelToolCalls = &parallelLimit
	}
	*settings = decoded
	return nil
}

func resolveSettings(settings Config) (agentloop.Settings, error) {
	parallelLimit := agentloop.DefaultMaxParallelToolCalls
	if settings.MaxParallelToolCalls != nil {
		parallelLimit = *settings.MaxParallelToolCalls
	}
	if parallelLimit < 1 {
		return agentloop.Settings{}, errors.New(
			"agentloop factory: maxParallelToolCalls must be a positive integer",
		)
	}
	return agentloop.Settings{
		MaxParallelToolCalls: parallelLimit,
	}, nil
}

func isNull(encoded json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(encoded), []byte("null"))
}
