package configuration_test

import (
	"strings"
	"testing"

	"github.com/gorenx/goren/plugin/configuration"
)

type limitFixture struct {
	MaxBodyBytes int64 `json:"maxBodyBytes"`
}

type configFixture struct {
	configuration.InputBase
	Address string       `json:"address"`
	Limits  limitFixture `json:"limits"`
}

func TestDecodeJSONRejectsAmbiguousOrUntypedInput(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		label       string
		input       string
		wantMessage string
	}{
		{
			label:       "unknown field",
			input:       `{"address":"127.0.0.1:3080","limits":{"maxBodyBytes":1},"extra":true}`,
			wantMessage: "unknown field",
		},
		{
			label:       "wrong type",
			input:       `{"address":7,"limits":{"maxBodyBytes":1}}`,
			wantMessage: "cannot unmarshal",
		},
		{
			label:       "duplicate field",
			input:       `{"address":"127.0.0.1:1","address":"127.0.0.1:2","limits":{"maxBodyBytes":1}}`,
			wantMessage: "duplicate field",
		},
		{
			label:       "nested duplicate",
			input:       `{"address":"127.0.0.1:1","limits":{"maxBodyBytes":1,"maxBodyBytes":2}}`,
			wantMessage: "duplicate field",
		},
		{
			label:       "dynamic expression",
			input:       `!!js/function (() => ({ address: "127.0.0.1:3080" }))`,
			wantMessage: "invalid JSON",
		},
		{
			label:       "multiple values",
			input:       `{}` + "\n" + `{}`,
			wantMessage: "multiple JSON values",
		},
	} {
		selectedCase := testCase
		t.Run(selectedCase.label, func(t *testing.T) {
			t.Parallel()
			sourceDocument, err := configuration.NewDocument([]byte(selectedCase.input))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = configuration.DecodeJSON[configFixture](sourceDocument); err == nil ||
				!strings.Contains(err.Error(), selectedCase.wantMessage) {
				t.Fatalf("DecodeJSON error = %v, want containing %q", err, selectedCase.wantMessage)
			}
		})
	}
}

func TestDecodeJSONReturnsNamedConfigurationWithoutValidationSideEffects(t *testing.T) {
	t.Parallel()
	sourceDocument, err := configuration.NewDocument(
		[]byte(`{"address":"127.0.0.1:3080","limits":{"maxBodyBytes":1024}}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := configuration.DecodeJSON[configFixture](sourceDocument)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Address != "127.0.0.1:3080" || settings.Limits.MaxBodyBytes != 1024 {
		t.Fatalf("decoded configuration = %#v", settings)
	}
}
