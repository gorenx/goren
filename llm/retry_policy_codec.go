package llm

import (
	"bytes"
	"encoding/json"
	"errors"
)

type retryPolicyWire struct {
	Mode           json.RawMessage `json:"mode"`
	MaxRetries     json.RawMessage `json:"maxRetries"`
	RetryableCodes json.RawMessage `json:"retryableCodes"`
	Backoff        json.RawMessage `json:"backoff"`
}

// UnmarshalJSON preserves omission and enforces the source tagged-union keys.
func (settings *RetryPolicyConfig) UnmarshalJSON(encoded []byte) error {
	if settings == nil {
		return errors.New("llm: cannot decode retryPolicy into nil target")
	}
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return errors.New("llm: retryPolicy must be an object")
	}
	var wireValue retryPolicyWire
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wireValue); err != nil {
		return err
	}
	var decoded RetryPolicyConfig
	if len(wireValue.Mode) == 0 || bytes.Equal(bytes.TrimSpace(wireValue.Mode), []byte("null")) {
		return errors.New("llm: retryPolicy.mode must be normal or always")
	}
	if err := json.Unmarshal(wireValue.Mode, &decoded.Mode); err != nil {
		return errors.New("llm: retryPolicy.mode must be normal or always")
	}
	if len(wireValue.MaxRetries) != 0 {
		if bytes.Equal(bytes.TrimSpace(wireValue.MaxRetries), []byte("null")) {
			return errors.New("llm: retryPolicy.maxRetries must be a non-negative safe integer")
		}
		var retryLimit int64
		if err := json.Unmarshal(wireValue.MaxRetries, &retryLimit); err != nil {
			return errors.New("llm: retryPolicy.maxRetries must be a non-negative safe integer")
		}
		decoded.MaxRetries = &retryLimit
	}
	if len(wireValue.RetryableCodes) != 0 {
		if bytes.Equal(bytes.TrimSpace(wireValue.RetryableCodes), []byte("null")) {
			return errors.New("llm: retryPolicy.retryableCodes must be an array")
		}
		if err := json.Unmarshal(wireValue.RetryableCodes, &decoded.RetryableCodes); err != nil {
			return errors.New("llm: retryPolicy.retryableCodes must be an array")
		}
	}
	if len(wireValue.Backoff) != 0 {
		if bytes.Equal(bytes.TrimSpace(wireValue.Backoff), []byte("null")) {
			return errors.New("llm: retryPolicy.backoff must be an object")
		}
		var backoffSettings BackoffConfig
		decoder = json.NewDecoder(bytes.NewReader(wireValue.Backoff))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&backoffSettings); err != nil {
			return err
		}
		decoded.Backoff = &backoffSettings
	}
	*settings = decoded
	return nil
}
