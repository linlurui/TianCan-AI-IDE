//go:build production

package main

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var frontendFS embed.FS

func buildAssetHandler() http.Handler {
	sub, err := fs.Sub(frontendFS, "frontend/dist")
	if err != nil {
		panic(err)
	}
	return application.BundledAssetFileServer(sub)
}
