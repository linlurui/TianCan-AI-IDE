//go:build !production

package main

import (
	"net/http"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// buildAssetHandler returns a handler for dev mode.
// When FRONTEND_DEVSERVER_URL=http://localhost:34115 is set (by wails3 dev),
// BundledAssetFileServer automatically proxies to the Vite dev server.
func buildAssetHandler() http.Handler {
	return application.BundledAssetFileServer(os.DirFS("frontend/dist"))
}
