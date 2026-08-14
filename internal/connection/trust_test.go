package connection

import (
	"net/http/httptest"
	"testing"
)

func TestTrustedAPIRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		host         string
		origin       string
		fetchSite    string
		trustedHosts []string
		want         bool
	}{
		{name: "localhost", host: "localhost:3080", want: true},
		{name: "ipv4 loopback range", host: "127.8.9.10:80", origin: "http://127.8.9.10:80", want: true},
		{name: "ipv6 loopback", host: "[::1]:3080", origin: "http://[::1]:3080", want: true},
		{name: "trusted any port", host: "192.168.1.5:3080", trustedHosts: []string{"192.168.1.5"}, want: true},
		{name: "trusted exact port", host: "harness.internal:3080", origin: "http://harness.internal:3080", trustedHosts: []string{"harness.internal:3080"}, want: true},
		{name: "wrong trusted port", host: "harness.internal:9999", trustedHosts: []string{"harness.internal:3080"}},
		{name: "untrusted host", host: "evil.example:3080", origin: "http://evil.example:3080"},
		{name: "origin mismatch", host: "127.0.0.1:3080", origin: "http://evil.example"},
		{name: "cross site", host: "127.0.0.1:3080", fetchSite: "cross-site"},
		{name: "invalid ipv4", host: "127.0.0.999"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			trusted, err := validateTrustedHosts(testCase.trustedHosts)
			if err != nil {
				t.Fatal(err)
			}
			httpRequest := httptest.NewRequest("GET", "http://localhost/api/events.mux", nil)
			httpRequest.Host = testCase.host
			if testCase.origin != "" {
				httpRequest.Header.Set("Origin", testCase.origin)
			}
			if testCase.fetchSite != "" {
				httpRequest.Header.Set("Sec-Fetch-Site", testCase.fetchSite)
			}
			if got := isTrustedAPIRequest(httpRequest, trusted); got != testCase.want {
				t.Fatalf("isTrustedAPIRequest() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestTrustedHostConfigRejectsNonAuthorities(t *testing.T) {
	t.Parallel()
	invalid := []string{
		"harness.internal/path", "user@harness.internal", "harness.internal:3080 ",
		"harness.internal:", "harness.internal:0080", "0x7f.0.0.1", "[0:0:0:0:0:0:0:1]",
	}
	for _, entry := range invalid {
		t.Run(entry, func(t *testing.T) {
			t.Parallel()
			if _, err := validateTrustedHosts([]string{entry}); err == nil {
				t.Fatal("invalid trusted host was accepted")
			}
		})
	}
}
