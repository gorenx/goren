// Package web serves the self-contained browser UI for the main
// Agent conversation flow. API and WebSocket routes remain owned by Connection.
package web

import (
	"context"
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/gorenx/goren/plugin"
)

const (
	// PluginName identifies the embedded Web frontend Plugin.
	PluginName = "@gorenx/dsh-web"
	// ServiceName identifies the frontend HTTP capability.
	ServiceName = "webFrontend"
)

// Frontend is the embedded browser handler consumed by Connection.
type Frontend interface {
	plugin.Service
	http.Handler
}

//go:embed dist/*
var builtAssets embed.FS

// Site serves the embedded conversation shell and its immutable assets.
type Site struct {
	plugin.Base
	files fs.FS
}

// New returns the Vite production build without a runtime filesystem or
// TypeScript source-checkout dependency.
func New() *Site {
	assetFiles, err := fs.Sub(builtAssets, "dist")
	if err != nil {
		panic(err)
	}
	return &Site{
		files: assetFiles,
	}
}

// Manifest provides the embedded browser frontend capability.
func (owner *Site) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[Frontend](owner),
		},
	}
}

// Apply validates startup cancellation before frontend publication.
func (*Site) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

// Dispose has no resource to release because assets are embedded and immutable.
func (*Site) Dispose(context.Context) error {
	return nil
}

// ServeHTTP serves GET/HEAD browser resources. Unknown browser routes fall
// back to the shell, while API paths never escape Connection ownership.
func (owner *Site) ServeHTTP(responseWriter http.ResponseWriter, httpRequest *http.Request) {
	if httpRequest.Method != http.MethodGet && httpRequest.Method != http.MethodHead {
		http.NotFound(responseWriter, httpRequest)
		return
	}
	requestPath := httpRequest.URL.Path
	if strings.HasPrefix(requestPath, "/api") {
		http.NotFound(responseWriter, httpRequest)
		return
	}
	resourcePath := strings.TrimPrefix(path.Clean(requestPath), "/")
	if resourcePath == "." || resourcePath == "" || resourcePath == "index.html" {
		owner.writeAsset(responseWriter, httpRequest, "index.html", "no-cache")
		return
	}
	if _, err := fs.Stat(owner.files, resourcePath); err == nil {
		cacheControl := "no-cache"
		if strings.HasPrefix(resourcePath, "assets/") {
			cacheControl = "public, max-age=31536000, immutable"
		}
		owner.writeAsset(responseWriter, httpRequest, resourcePath, cacheControl)
		return
	}
	if strings.Contains(path.Base(resourcePath), ".") {
		http.NotFound(responseWriter, httpRequest)
		return
	}
	owner.writeAsset(responseWriter, httpRequest, "index.html", "no-cache")
}

func (owner *Site) writeAsset(
	responseWriter http.ResponseWriter,
	httpRequest *http.Request,
	resourcePath string,
	cacheControl string,
) {
	content, err := fs.ReadFile(owner.files, resourcePath)
	if err != nil {
		http.Error(responseWriter, "frontend asset unavailable", http.StatusInternalServerError)
		return
	}
	mediaType := mime.TypeByExtension(path.Ext(resourcePath))
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	if resourcePath == "index.html" {
		mediaType = "text/html; charset=utf-8"
	}
	responseWriter.Header().Set("content-type", mediaType)
	responseWriter.Header().Set("cache-control", cacheControl)
	responseWriter.Header().Set("x-content-type-options", "nosniff")
	responseWriter.Header().Set("referrer-policy", "no-referrer")
	responseWriter.WriteHeader(http.StatusOK)
	if httpRequest.Method == http.MethodHead {
		return
	}
	_, _ = responseWriter.Write(content)
}
