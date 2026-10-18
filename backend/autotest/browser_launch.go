package autotest

import (
	"context"
	"fmt"
	"os"

	"github.com/go-rod/rod/lib/launcher"

	"github.com/linlurui/tiancan-ai-ide/backend/browser"
)

// launchBrowser starts a headless Chrome with a fresh profile and returns its
// browser-level WebSocket URL plus a cleanup func.
//
// Why rod's launcher but not rod's Browser/Page: the launcher only picks the
// right Chromium flags (sandbox, GPU, mock keychain, …) and execs the process —
// that part works fine and is easy to get wrong by hand (a bare exec dies with
// "sandbox initialization failed" / "GPU process isn't usable"). The rod
// high-level API is what we're avoiding (see cdp.go). So: launcher for the
// process, our own CDP client for the protocol.
func launchBrowser(ctx context.Context, bin string) (controlURL string, cleanup func(), err error) {
	profile, err := os.MkdirTemp("", "tiancan-autotest-")
	if err != nil {
		return "", nil, fmt.Errorf("create profile dir: %w", err)
	}

	// --no-sandbox --disable-gpu --use-mock-keychain：缺了这三个，Chrome 会在
	// 打印 DevTools 行之后立刻崩溃退出（sandbox initialization failed / GPU
	// process isn't usable），客户端拿到的是一条秒断的连接。
	l := launcher.New().Bin(bin).HeadlessNew(true).UserDataDir(profile).
		Set("no-sandbox").
		Set("disable-gpu").
		Set("use-mock-keychain")

	url, err := l.Launch()
	if err != nil {
		os.RemoveAll(profile)
		return "", nil, fmt.Errorf("launch browser: %w", err)
	}

	// ctx 取消（StopRun）时杀浏览器；cleanup 收尾 profile 目录。
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			l.Kill()
		case <-stop:
		}
	}()
	cleanup = func() {
		close(stop)
		l.Kill()
		_ = os.RemoveAll(profile)
	}
	return url, cleanup, nil
}

// resolveBin finds the browser to use, mirroring the shared resolver.
func resolveBin() (string, error) {
	if bin := browser.ResolveBin(); bin != "" {
		return bin, nil
	}
	return "", browser.ErrNoBrowser
}
