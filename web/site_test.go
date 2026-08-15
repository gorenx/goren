package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSiteServesEmbeddedMainFlowAndSPAFallback(t *testing.T) {
	browserAssets := New()

	indexResponse := requestSite(browserAssets, http.MethodGet, "/")
	indexBody := indexResponse.Body.String()
	if indexResponse.Code != http.StatusOK ||
		!strings.Contains(indexBody, "Goren") ||
		!strings.Contains(indexBody, "session-list") ||
		!strings.Contains(indexBody, "/app.js") {
		t.Fatalf("index response = (%d, %q)", indexResponse.Code, indexBody)
	}

	scriptResponse := requestSite(browserAssets, http.MethodGet, "/app.js")
	if scriptResponse.Code != http.StatusOK ||
		!strings.Contains(scriptResponse.Body.String(), "session.prompt") ||
		!strings.Contains(scriptResponse.Body.String(), "/api/events.mux") ||
		!strings.HasPrefix(scriptResponse.Header().Get("content-type"), "text/javascript") {
		t.Fatalf("script response = (%d, %q, %q)", scriptResponse.Code, scriptResponse.Header().Get("content-type"), scriptResponse.Body.String())
	}

	styleResponse := requestSite(browserAssets, http.MethodHead, "/app.css")
	if styleResponse.Code != http.StatusOK || styleResponse.Body.Len() != 0 ||
		!strings.HasPrefix(styleResponse.Header().Get("content-type"), "text/css") {
		t.Fatalf("style response = (%d, %q, %q)", styleResponse.Code, styleResponse.Header().Get("content-type"), styleResponse.Body.String())
	}

	spaResponse := requestSite(browserAssets, http.MethodGet, "/sessions/example")
	if spaResponse.Code != http.StatusOK || !strings.Contains(spaResponse.Body.String(), "session-list") {
		t.Fatalf("SPA response = (%d, %q)", spaResponse.Code, spaResponse.Body.String())
	}
	for _, rejectedPath := range []string{"/api/unknown", "/missing.js"} {
		response := requestSite(browserAssets, http.MethodGet, rejectedPath)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d", rejectedPath, response.Code)
		}
	}
}

func requestSite(browserAssets http.Handler, method string, requestPath string) *httptest.ResponseRecorder {
	httpRequest := httptest.NewRequest(method, "http://localhost"+requestPath, nil)
	responseRecorder := httptest.NewRecorder()
	browserAssets.ServeHTTP(responseRecorder, httpRequest)
	return responseRecorder
}
