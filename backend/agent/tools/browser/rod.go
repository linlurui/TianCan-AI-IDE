package browser

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	sharedbrowser "github.com/linlurui/tiancan-ai-ide/backend/browser"
)

// defaultTimeout bounds every CDP call so a hung page cannot stall the agent loop.
const defaultTimeout = 30 * time.Second

// rodManager is a Manager backed by go-rod (pure-Go CDP, no external Node runtime).
// Every input it produces is a trusted CDP event (isTrusted=true); the generic
// Evaluate path is additionally guarded by IsSyntheticInputScript.
type rodManager struct {
	mu       sync.Mutex
	browser  *rod.Browser
	sessions map[string]*rodSession
	nextID   int
	binPath  string
	headless bool
}

// Option configures a rod-backed Manager.
type Option func(*rodManager)

// WithBinPath points the launcher at an explicit browser executable.
func WithBinPath(p string) Option { return func(m *rodManager) { m.binPath = p } }

// WithHeadless controls whether the browser runs headless (default true).
func WithHeadless(h bool) Option { return func(m *rodManager) { m.headless = h } }

// NewManager creates a rod-backed browser Manager. The browser process is
// launched lazily on the first NewSession.
func NewManager(opts ...Option) Manager {
	m := &rodManager{
		sessions: make(map[string]*rodSession),
		headless: true,
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// launch starts (or reuses) the rod browser process.
//
// Browser discovery and process startup live in the shared driver
// (backend/browser) so this package and the webai login/chat flows cannot drift
// apart. That matters: the driver deliberately refuses to let go-rod
// auto-download a Chromium build, which otherwise turns "no browser installed"
// into an indefinite network hang.
func (m *rodManager) launch() (*rod.Browser, error) {
	opts := sharedbrowser.Options{Headless: m.headless, Bin: m.binPath}
	if opts.Bin == "" {
		// Fall back to an operator-pinned binary via the shared resolver.
		opts.Bin = sharedbrowser.ResolveBin()
	}
	b, err := sharedbrowser.Launch(opts)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoBrowser, err)
	}
	return b, nil
}

// NewSession launches/allocates a blank page and returns its id and handle.
func (m *rodManager) NewSession() (string, Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.browser == nil {
		b, err := m.launch()
		if err != nil {
			return "", nil, err
		}
		m.browser = b
	}
	page, err := m.browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return "", nil, fmt.Errorf("create page: %w", err)
	}
	id := fmt.Sprintf("s%d", m.nextID)
	m.nextID++
	s := &rodSession{page: page}
	m.sessions[id] = s
	return id, s, nil
}

// GetSession returns an existing session by id.
func (m *rodManager) GetSession(id string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return s, nil
}

// CloseAll releases every session and shuts down the browser.
func (m *rodManager) CloseAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.sessions {
		_ = s.Close()
		delete(m.sessions, id)
	}
	if m.browser != nil {
		m.browser.MustClose()
		m.browser = nil
	}
	return nil
}

// rodSession is a single rod page.
type rodSession struct {
	page *rod.Page
}

// ctxPage binds the per-call context and timeout to the page.
func (s *rodSession) ctxPage(ctx context.Context) *rod.Page {
	return s.page.Timeout(defaultTimeout).Context(ctx)
}

// Navigate loads a URL and waits for load.
func (s *rodSession) Navigate(ctx context.Context, url string) error {
	p := s.ctxPage(ctx)
	if err := p.Navigate(url); err != nil {
		return fmt.Errorf("navigate %q: %w", url, err)
	}
	if err := p.WaitLoad(); err != nil {
		return fmt.Errorf("wait load %q: %w", url, err)
	}
	return nil
}

// Click clicks the first element matching selector (trusted CDP mouse input).
func (s *rodSession) Click(ctx context.Context, selector string) error {
	el, err := s.ctxPage(ctx).Element(selector)
	if err != nil {
		return fmt.Errorf("find %q: %w", selector, err)
	}
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("click %q: %w", selector, err)
	}
	return nil
}

// Type focuses the element and inserts text as trusted key input.
func (s *rodSession) Type(ctx context.Context, selector, text string) error {
	el, err := s.ctxPage(ctx).Element(selector)
	if err != nil {
		return fmt.Errorf("find %q: %w", selector, err)
	}
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("focus %q: %w", selector, err)
	}
	if err := s.ctxPage(ctx).InsertText(text); err != nil {
		return fmt.Errorf("type into %q: %w", selector, err)
	}
	return nil
}

// Fill selects all existing text and replaces it with trusted key input.
func (s *rodSession) Fill(ctx context.Context, selector, text string) error {
	el, err := s.ctxPage(ctx).Element(selector)
	if err != nil {
		return fmt.Errorf("find %q: %w", selector, err)
	}
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("focus %q: %w", selector, err)
	}
	if err := el.SelectAllText(); err != nil {
		return fmt.Errorf("select %q: %w", selector, err)
	}
	if err := s.ctxPage(ctx).InsertText(text); err != nil {
		return fmt.Errorf("fill %q: %w", selector, err)
	}
	return nil
}

// Screenshot returns a PNG snapshot of the page.
func (s *rodSession) Screenshot(ctx context.Context) ([]byte, error) {
	data, err := s.ctxPage(ctx).Screenshot(false, nil)
	if err != nil {
		return nil, fmt.Errorf("screenshot: %w", err)
	}
	return data, nil
}

// Evaluate runs read-only JS. Scripts that would synthesize input events are
// rejected with ErrSyntheticInput (the isTrusted hard wall).
func (s *rodSession) Evaluate(ctx context.Context, expr string) (string, error) {
	if blocked, reason := IsSyntheticInputScript(expr); blocked {
		return "", fmt.Errorf("%w: %s", ErrSyntheticInput, reason)
	}
	res, err := s.ctxPage(ctx).Eval(wrapAsFunction(expr))
	if err != nil {
		return "", fmt.Errorf("evaluate: %w", err)
	}
	return renderEval(res.Value), nil
}

// wrapAsFunction and renderEval delegate to the shared driver so every rod
// consumer adapts scripts and renders CDP values identically. Keeping them as
// package-level vars preserves the unit tests that pin these behaviours.
var (
	wrapAsFunction = sharedbrowser.WrapFunc
	renderEval     = sharedbrowser.RenderValue
)

// HTML returns the page's serialized HTML.
func (s *rodSession) HTML(ctx context.Context) (string, error) {
	html, err := s.ctxPage(ctx).HTML()
	if err != nil {
		return "", fmt.Errorf("html: %w", err)
	}
	return html, nil
}

// Text returns the page's visible text content.
func (s *rodSession) Text(ctx context.Context) (string, error) {
	el, err := s.ctxPage(ctx).Element("body")
	if err != nil {
		return "", fmt.Errorf("find body: %w", err)
	}
	text, err := el.Text()
	if err != nil {
		return "", fmt.Errorf("text: %w", err)
	}
	return text, nil
}

// Title returns the page title.
func (s *rodSession) Title(ctx context.Context) (string, error) {
	info, err := s.ctxPage(ctx).Info()
	if err != nil {
		return "", fmt.Errorf("info: %w", err)
	}
	return info.Title, nil
}

// Close releases the page.
func (s *rodSession) Close() error {
	s.page.MustClose()
	return nil
}

// renderEval converts a CDP evaluate result into a stable string. It is
// implemented by the shared driver (backend/browser.RenderValue).
// renderEval is assigned to the shared implementation above.
