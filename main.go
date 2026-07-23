package main

import (
	"log"
	"os"

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
	"github.com/rocky233/tiancan-ai-ide/backend/window"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func main() {
	fsService := &filesystem.Service{}
	extService := extension.NewService()
	lspService := lsp.NewService()
	cfgService := config.NewService()
	aiService := ai.NewService()
	winManager := window.NewManager()
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
			application.NewService(winManager),
		},
		Assets: application.AssetOptions{
			Handler: buildAssetHandler(),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	// Multi-window architecture: configurable via TIANCAN_MULTI_WINDOW env
	// When set to "1", opens separate windows for editor, terminal, AI, and debug.
	// Default: single unified window (original behavior).
	if os.Getenv("TIANCAN_MULTI_WINDOW") == "1" {
		createMultiWindows(app, winManager)
	} else {
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
	}

	fsService.App = app
	aiService.App = app
	pwService.SetApp(app)
	winManager.SetApp(app)
	// Wire SmartRouter App so it can emit events (model status, kill-lm-studio, etc.)
	cfg := aiService.Router.GetConfig()
	cfg.App = app
	aiService.Router.SetConfig(cfg)
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

// createMultiWindows opens separate independent windows for editor, terminal, AI panel, and debug panel.
// Each window has its own WebView context and lifecycle management.
// Windows communicate via the shared Wails application services.
func createMultiWindows(app *application.App, wm *window.Manager) {
	// Main editor window (largest, acts as primary)
	wm.RegisterWindow("editor", "天蚕 IDE — 编辑器", "editor", 1000, 800)
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:           "天蚕 IDE — 编辑器",
		Width:           1000,
		Height:          800,
		MinWidth:        700,
		MinHeight:       500,
		DevToolsEnabled: true,
		URL:             "/?window=editor",
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInsetUnified,
		},
	})

	wm.RegisterWindow("terminal", "天蚕 IDE — 终端", "terminal", 700, 500)
	// Terminal window
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:           "天蚕 IDE — 终端",
		Width:           700,
		Height:          500,
		MinWidth:        400,
		MinHeight:       300,
		DevToolsEnabled: true,
		URL:             "/?window=terminal",
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInsetUnified,
		},
	})

	wm.RegisterWindow("ai", "天蚕 IDE — AI 助手", "ai", 500, 700)
	// AI panel window
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:           "天蚕 IDE — AI 助手",
		Width:           500,
		Height:          700,
		MinWidth:        350,
		MinHeight:       400,
		DevToolsEnabled: true,
		URL:             "/?window=ai",
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInsetUnified,
		},
	})

	wm.RegisterWindow("debug", "天蚕 IDE — 调试", "debug", 600, 500)
	// Debug panel window
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:           "天蚕 IDE — 调试",
		Width:           600,
		Height:          500,
		MinWidth:        400,
		MinHeight:       300,
		DevToolsEnabled: true,
		URL:             "/?window=debug",
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInsetUnified,
		},
	})
}
