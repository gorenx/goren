package apiproxy

import (
	"strings"
	"testing"
)

func TestDecodeSessionSearchRequestUsesJavaScriptStringLength(t *testing.T) {
	t.Parallel()
	accepted, issues := DecodeSessionSearchRequest([]byte(`{"query":"  exact phrase  "}`))
	if len(issues) != 0 || accepted.Query != "exact phrase" {
		t.Fatalf("accepted query = %#v, issues = %#v", accepted, issues)
	}
	for _, input := range []string{
		`{"query":"   "}`,
		`{"query":"bad\u0000query"}`,
		`{"query":"` + strings.Repeat("😀", 251) + `"}`,
	} {
		if _, issues := DecodeSessionSearchRequest([]byte(input)); len(issues) == 0 {
			t.Fatalf("invalid query accepted: %q", input)
		}
	}
}
