package autotest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/linlurui/tiancan-ai-ide/backend/browser"
	"github.com/linlurui/tiancan-ai-ide/backend/events"
)

// Service is the Wails-bound browser-test service.
type Service struct {
	mu     sync.Mutex
	app    *application.App // G3 起为 sink 兜底桥
	sink   events.Sink      // 非空时优先于 app
	cancel context.CancelFunc
	running bool
}

// NewService creates the service.
func NewService() *Service { return &Service{} }

// SetApp sets the Wails app reference for event emission.
func (s *Service) SetApp(app *application.App) { s.app = app }

// SetSink injects a ProgressSink (PC-side / tests); takes priority over app.
func (s *Service) SetSink(sink events.Sink) { s.sink = sink }

// emit fires an event: sink first, then the Wails bridge, then silent default.
func (s *Service) emit(event string, payload interface{}) {
	if s.sink != nil {
		s.sink.Emit(event, payload)
		return
	}
	if s.app != nil {
		s.app.Event.Emit(event, payload)
		return
	}
	events.Default().Emit(event, payload)
}

// BrowserReady reports whether a local Chrome/Chromium can be driven.
func (s *Service) BrowserReady() bool { return browser.Available() }

// BrowserBin returns the browser binary that will be used ("" when none).
func (s *Service) BrowserBin() string { return browser.ResolveBin() }

// ListScripts returns every saved test for a project root.
func (s *Service) ListScripts(projectRoot string) []Script {
	return ListScripts(projectRoot)
}

// SaveScript validates and stores one test.
func (s *Service) SaveScript(projectRoot string, sc Script) error {
	return SaveScript(projectRoot, sc)
}

// DeleteScript removes one test.
func (s *Service) DeleteScript(projectRoot, id string) error {
	return DeleteScript(projectRoot, id)
}

// RunScript runs one test in the background, streaming "autotest:output"
// events. Returns an error only when the run cannot be started.
func (s *Service) RunScript(projectRoot, id string) error {
	sc, err := LoadScript(projectRoot, id)
	if err != nil {
		return err
	}
	if err := sc.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("已有测试在运行")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.running = true
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			s.running = false
			s.cancel = nil
			s.mu.Unlock()
		}()

		rep := func(ev RunEvent) {
			if data, err := json.Marshal(ev); err == nil {
				s.emit("autotest:output", string(data))
			}
		}

		start := time.Now()
		runErr := (&Runner{}).Run(ctx, sc, ShotsDir(projectRoot), rep)
		rep(RunEvent{
			ScriptID: sc.ID, Type: "done",
			OK: runErr == nil, Ms: time.Since(start).Milliseconds(), Total: len(sc.Steps),
			Line: func() string {
				if runErr != nil {
					return runErr.Error()
				}
				return fmt.Sprintf("全部 %d 步通过", len(sc.Steps))
			}(),
		})
	}()
	return nil
}

// StopRun cancels the running test (its browser is closed mid-run).
func (s *Service) StopRun() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.cancel == nil {
		return fmt.Errorf("没有正在运行的测试")
	}
	s.cancel()
	return nil
}

// IsRunning reports whether a test is currently executing.
func (s *Service) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}
