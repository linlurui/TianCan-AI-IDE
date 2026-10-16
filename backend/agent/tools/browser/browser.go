package browser

import (
	"context"
	"errors"
)

// ErrSyntheticInput is returned by Session.Evaluate when the script would
// synthesize isTrusted=false input events (blocked by the isTrusted hard wall).
var ErrSyntheticInput = errors.New("browser.evaluate blocked: script would synthesize untrusted (isTrusted=false) input")

// ErrSessionNotFound is returned when an unknown session id is requested.
var ErrSessionNotFound = errors.New("browser session not found")

// ErrNoBrowser is returned when no browser binary can be located/launched.
var ErrNoBrowser = errors.New("no browser binary available (install Chrome/Chromium or set the browser path)")

// Session is a single controlled browser page. All input it produces is trusted
// (rod drives the browser via CDP real input). The only generic escape hatch is
// Evaluate, whose script is run through the isTrusted hard wall (IsSyntheticInputScript).
type Session interface {
	// Navigate loads a URL.
	Navigate(ctx context.Context, url string) error
	// Click clicks the first element matching selector (trusted CDP input).
	Click(ctx context.Context, selector string) error
	// Type clicks the element, focuses it, and types text (trusted CDP input).
	Type(ctx context.Context, selector, text string) error
	// Fill clears the element then types text (trusted CDP input).
	Fill(ctx context.Context, selector, text string) error
	// Screenshot returns a PNG snapshot of the page.
	Screenshot(ctx context.Context) ([]byte, error)
	// Evaluate runs read-only JS in the page. Scripts that synthesize input
	// events are rejected with ErrSyntheticInput.
	Evaluate(ctx context.Context, expr string) (string, error)
	// HTML returns the page's serialized HTML.
	HTML(ctx context.Context) (string, error)
	// Text returns the page's visible text content.
	Text(ctx context.Context) (string, error)
	// Title returns the page title.
	Title(ctx context.Context) (string, error)
	// Close releases the underlying page.
	Close() error
}

// Manager owns browser sessions. Implementations may pool a single headless
// browser process and multiplex sessions over it.
type Manager interface {
	// NewSession launches/allocates a page and returns its id and handle.
	NewSession() (id string, s Session, err error)
	// GetSession returns an existing session by id.
	GetSession(id string) (Session, error)
	// CloseAll shuts down every session and the browser process.
	CloseAll() error
}

// noBrowserManager is a Manager that always fails — used as a safe default when
// no browser is available, so the tool fails closed instead of panicking.
type noBrowserManager struct{ reason error }

func (m *noBrowserManager) NewSession() (string, Session, error) {
	return "", nil, m.reason
}
func (m *noBrowserManager) GetSession(string) (Session, error) { return nil, m.reason }
func (m *noBrowserManager) CloseAll() error                    { return nil }

// NewNoBrowserManager returns a Manager that fails every call with ErrNoBrowser.
// Useful for tests and for environments without a browser binary.
func NewNoBrowserManager() Manager {
	return &noBrowserManager{reason: ErrNoBrowser}
}
