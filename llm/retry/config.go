package llmretry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// Config is intentionally empty. RetryPolicy belongs to each provider
// registration, not to this executor plugin.
type Config struct{}

// UnmarshalJSON rejects null, unknown executor settings, and the common error
// of placing retryPolicy at the consumer boundary.
func (settings *Config) UnmarshalJSON(encoded []byte) error {
	if settings == nil {
		return errors.New("llm-retry: cannot decode config into nil target")
	}
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return errors.New("llm-retry: config must be an object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return err
	}
	if fields == nil {
		return errors.New("llm-retry: config must be an object")
	}
	if _, misplaced := fields["retryPolicy"]; misplaced {
		return errors.New("llm-retry: retryPolicy belongs under each provider configuration")
	}
	if len(fields) != 0 {
		names := make([]string, 0, len(fields))
		for fieldName := range fields {
			names = append(names, fieldName)
		}
		sort.Strings(names)
		return fmt.Errorf("llm-retry: unknown key %q", names[0])
	}
	*settings = Config{}
	return nil
}
