package mcpclient

import "testing"

func TestConfigurationRejectsAmbiguousOrUnsafeInputs(t *testing.T) {
	for _, data := range []string{
		`{"serverName":"test","transport":"stdio","command":"example","unknown":true}`,
		`{"serverName":"test","transport":"stdio","command":"example","url":"https://example.com"}`,
		`{"serverName":"test","transport":"streamable-http","url":"https://user:secret@example.com"}`,
		`{"serverName":"test","transport":"stdio","command":"example","requestTimeoutMs":-1}`,
		`{"serverName":"test","transport":"stdio","command":"example","maxPages":-1}`,
		`{"serverName":"test","transport":"stdio","command":"example"} {}`,
	} {
		if _, err := DecodeConfig([]byte(data)); err == nil {
			t.Fatalf("accepted invalid configuration: %s", data)
		}
	}
	if _, err := DecodeConfig([]byte(`{"serverName":"test","transport":"stdio","command":"example"}`)); err != nil {
		t.Fatal(err)
	}
}
