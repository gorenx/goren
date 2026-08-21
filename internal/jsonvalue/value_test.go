package jsonvalue

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestValidateLosslessJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		rawValue    json.RawMessage
		wantMessage string
	}{
		{
			name:     "nested value",
			rawValue: json.RawMessage(`{"text":"value","items":[1,true,null,{"nested":"ok"}]}`),
		},
		{
			name:        "duplicate field",
			rawValue:    json.RawMessage(`{"value":1,"value":2}`),
			wantMessage: `duplicate field "value" at $`,
		},
		{
			name:        "escaped duplicate field",
			rawValue:    json.RawMessage(`{"value":1,"\u0076alue":2}`),
			wantMessage: `duplicate field "value" at $`,
		},
		{
			name:        "negative zero",
			rawValue:    json.RawMessage(`{"items":[0,-0.0]}`),
			wantMessage: `invalid JSON number "-0.0" at $.items[1]`,
		},
		{
			name:        "overflow",
			rawValue:    json.RawMessage(`1e400`),
			wantMessage: `invalid JSON number "1e400" at $`,
		},
		{
			name:        "malformed",
			rawValue:    json.RawMessage(`{"value":`),
			wantMessage: "unexpected end of JSON input",
		},
		{
			name:        "trailing value",
			rawValue:    json.RawMessage(`{} {}`),
			wantMessage: "invalid character",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Validate(test.rawValue)
			if test.wantMessage == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("Validate error = %v, want %q", err, test.wantMessage)
			}
		})
	}
}

func TestCloneDetachesValidatedJSON(t *testing.T) {
	t.Parallel()
	source := json.RawMessage(`{"value":true}`)
	detached, err := Clone(source)
	if err != nil {
		t.Fatal(err)
	}
	source[0] = '['
	if string(detached) != `{"value":true}` {
		t.Fatalf("detached value = %s", detached)
	}
}

func TestValidateHandlesValuesBeyondInlineBuffers(t *testing.T) {
	t.Parallel()
	var wide strings.Builder
	wide.WriteByte('{')
	for fieldIndex := 0; fieldIndex < 40; fieldIndex++ {
		if fieldIndex > 0 {
			wide.WriteByte(',')
		}
		fmt.Fprintf(&wide, `"field-%d":%d`, fieldIndex, fieldIndex)
	}
	wide.WriteByte('}')
	if err := Validate(json.RawMessage(wide.String())); err != nil {
		t.Fatalf("wide object: %v", err)
	}
	wideDuplicate := strings.TrimSuffix(wide.String(), "}") + `,"field-1":40}`
	if err := Validate(json.RawMessage(wideDuplicate)); err == nil ||
		!strings.Contains(err.Error(), `duplicate field "field-1" at $`) {
		t.Fatalf("wide duplicate error = %v", err)
	}

	deepPrefix := strings.Repeat(`{"nested":`, 20)
	deepSuffix := strings.Repeat(`}`, 20)
	if err := Validate(json.RawMessage(deepPrefix + `true` + deepSuffix)); err != nil {
		t.Fatalf("deep value: %v", err)
	}
	deep := deepPrefix + `-0` + deepSuffix
	err := Validate(json.RawMessage(deep))
	if err == nil || !strings.Contains(
		err.Error(),
		strings.Repeat(".nested", 20),
	) {
		t.Fatalf("deep value error = %v", err)
	}
}
