package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestSiteServesEmbeddedMainFlowAndSPAFallback(t *testing.T) {
	browserAssets := New()

	indexResponse := requestSite(browserAssets, http.MethodGet, "/")
	indexBody := indexResponse.Body.String()
	if indexResponse.Code != http.StatusOK ||
		!strings.Contains(indexBody, "Goren") ||
		!strings.Contains(indexBody, `id="root"`) ||
		indexResponse.Header().Get("cache-control") != "no-cache" {
		t.Fatalf("index response = (%d, %q)", indexResponse.Code, indexBody)
	}

	scriptPath := findBuiltAsset(t, indexBody, "js")
	scriptResponse := requestSite(browserAssets, http.MethodGet, scriptPath)
	if scriptResponse.Code != http.StatusOK ||
		!strings.Contains(scriptResponse.Body.String(), "session.prompt") ||
		!strings.Contains(scriptResponse.Body.String(), "/api/events.mux") ||
		!strings.HasPrefix(scriptResponse.Header().Get("content-type"), "text/javascript") ||
		scriptResponse.Header().Get("cache-control") != "public, max-age=31536000, immutable" {
		t.Fatalf("script response = (%d, %q, %q)", scriptResponse.Code, scriptResponse.Header().Get("content-type"), scriptResponse.Body.String())
	}

	stylePath := findBuiltAsset(t, indexBody, "css")
	styleResponse := requestSite(browserAssets, http.MethodHead, stylePath)
	if styleResponse.Code != http.StatusOK || styleResponse.Body.Len() != 0 ||
		!strings.HasPrefix(styleResponse.Header().Get("content-type"), "text/css") ||
		styleResponse.Header().Get("cache-control") != "public, max-age=31536000, immutable" {
		t.Fatalf("style response = (%d, %q, %q)", styleResponse.Code, styleResponse.Header().Get("content-type"), styleResponse.Body.String())
	}

	spaResponse := requestSite(browserAssets, http.MethodGet, "/sessions/example")
	if spaResponse.Code != http.StatusOK || !strings.Contains(spaResponse.Body.String(), `id="root"`) {
		t.Fatalf("SPA response = (%d, %q)", spaResponse.Code, spaResponse.Body.String())
	}
	for _, rejectedPath := range []string{"/api/unknown", "/missing.js", "/app.js"} {
		response := requestSite(browserAssets, http.MethodGet, rejectedPath)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d", rejectedPath, response.Code)
		}
	}
}

func findBuiltAsset(t *testing.T, indexBody string, extension string) string {
	t.Helper()
	pattern := regexp.MustCompile(`['"](/assets/app-[^'"]+\.` + regexp.QuoteMeta(extension) + `)['"]`)
	matches := pattern.FindStringSubmatch(indexBody)
	if len(matches) != 2 {
		t.Fatalf("index has no hashed app.%s asset: %q", extension, indexBody)
	}
	return matches[1]
}

func requestSite(browserAssets http.Handler, method string, requestPath string) *httptest.ResponseRecorder {
	httpRequest := httptest.NewRequest(method, "http://localhost"+requestPath, nil)
	responseRecorder := httptest.NewRecorder()
	browserAssets.ServeHTTP(responseRecorder, httpRequest)
	return responseRecorder
}
