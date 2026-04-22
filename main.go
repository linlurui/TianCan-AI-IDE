package main

import (
	"log"

	"github.com/rocky233/tiancan-ai-ide/backend/ai"
	"github.com/rocky233/tiancan-ai-ide/backend/config"
	"github.com/rocky233/tiancan-ai-ide/backend/database"
	"github.com/rocky233/tiancan-ai-ide/backend/debug"
	"github.com/rocky233/tiancan-ai-ide/backend/deploy"
	"github.com/rocky233/tiancan-ai-ide/backend/extension"
	"github.com/rocky233/tiancan-ai-ide/backend/filesystem"
	"github.com/rocky233/tiancan-ai-ide/backend/git"
	"github.com/rocky233/tiancan-ai-ide/backend/lsp"
	"github.com/rocky233/tiancan-ai-ide/backend/playwright"
	"github.com/rocky233/tiancan-ai-ide/backend/process"
	"github.com/rocky233/tiancan-ai-ide/backend/project"
	"github.com/rocky233/tiancan-ai-ide/backend/remote"
	"github.com/rocky233/tiancan-ai-ide/backend/terminal"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func main() {
	fsService := &filesystem.Service{}
	extService := extension.NewService()
	lspService := lsp.NewService()
	cfgService := config.NewService()
	aiService := ai.NewService()
	pwService := playwright.NewService()

	app := application.New(application.Options{
		Name:        "TianCan AI IDE",
		Description: "纯本地 AI 编程 IDE",
		Services: []application.Service{
			application.NewService(fsService),
			application.NewService(&git.Service{}),
			application.NewService(&process.Service{}),
			application.NewService(extService),
			application.NewService(terminal.NewService()),
			application.NewService(project.NewService()),
			application.NewService(lspService),
			application.NewService(database.NewService()),
			application.NewService(deploy.NewService()),
			application.NewService(debug.NewService()),
			application.NewService(cfgService),
			application.NewService(remote.NewService()),
			application.NewService(pwService),
			application.NewService(aiService),
		},
		Assets: application.AssetOptions{
			Handler: buildAssetHandler(),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	// TODO: Implement multi-window architecture — separate independent windows for editor, terminal, AI panel, debug panel
	// TODO: Each window should have its own WebView context and lifecycle management
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:           "天蚕 AI IDE",
		Width:           1400,
		Height:          900,
		MinWidth:        900,
		MinHeight:       600,
		DevToolsEnabled: true,
		URL:             "/",
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInsetUnified,
		},
	})

	fsService.App = app
	aiService.App = app
	pwService.SetApp(app)
	cfgService.StartWatcher()

	// Wire LSP bin dir from settings (and keep in sync on every save).
	syncLspBinDir := func() {
		s := cfgService.GetSettings()
		lspService.SetBinDir(cfgService.GetLspBinDir(), s.LspPaths)
	}
	syncLspBinDir()

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
