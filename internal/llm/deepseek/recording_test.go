package deepseek

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

type httpRecording struct {
	statusCode int
	header     http.Header
	body       []byte
}

func loadHTTPRecording(testingContext *testing.T, fixtureName string) httpRecording {
	testingContext.Helper()
	fixturePath := filepath.Join("testdata", "recordings", fixtureName)
	encoded, err := os.ReadFile(fixturePath)
	if err != nil {
		testingContext.Fatalf("read HTTP recording %q: %v", fixturePath, err)
	}
	fixtureRequest, err := http.NewRequest(http.MethodPost, "https://api.deepseek.com/chat/completions", nil)
	if err != nil {
		testingContext.Fatalf("create recording request: %v", err)
	}
	wireResponse, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(encoded)), fixtureRequest)
	if err != nil {
		testingContext.Fatalf("parse HTTP recording %q: %v", fixturePath, err)
	}
	defer wireResponse.Body.Close()
	bodyBytes, err := io.ReadAll(wireResponse.Body)
	if err != nil {
		testingContext.Fatalf("read HTTP recording body %q: %v", fixturePath, err)
	}
	return httpRecording{
		statusCode: wireResponse.StatusCode,
		header:     wireResponse.Header.Clone(),
		body:       bodyBytes,
	}
}

func replayHTTPRecording(responseWriter http.ResponseWriter, recording httpRecording) {
	for headerName, values := range recording.header {
		for _, value := range values {
			responseWriter.Header().Add(headerName, value)
		}
	}
	responseWriter.WriteHeader(recording.statusCode)
	_, _ = responseWriter.Write(recording.body)
}
