package browser

import (
	"context"
	"errors"
	"testing"
)

// fakeSession records calls and mirrors the production guard contract:
// Evaluate rejects synthetic-input scripts, otherwise returns a canned value.
type fakeSession struct {
	navigated   []string
	clicks      []string
	typed       []string
	filled      []string
	closed      bool
	htmlOut     string
	textOut     string
	titleOut    string
	evalOut     string
	evalBlocked bool // when true, Evaluate returns ErrSyntheticInput regardless
}

func (f *fakeSession) Navigate(_ context.Context, url string) error {
	f.navigated = append(f.navigated, url)
	return nil
}
func (f *fakeSession) Click(_ context.Context, sel string) error {
	f.clicks = append(f.clicks, sel)
	return nil
}
func (f *fakeSession) Type(_ context.Context, sel, text string) error {
	f.typed = append(f.typed, sel+"=>"+text)
	return nil
}
func (f *fakeSession) Fill(_ context.Context, sel, text string) error {
	f.filled = append(f.filled, sel+"=>"+text)
	return nil
}
func (f *fakeSession) Screenshot(context.Context) ([]byte, error) { return []byte("png"), nil }
func (f *fakeSession) Evaluate(_ context.Context, expr string) (string, error) {
	if f.evalBlocked {
		return "", ErrSyntheticInput
	}
	if ok, _ := IsSyntheticInputScript(expr); ok {
		return "", ErrSyntheticInput
	}
	return f.evalOut, nil
}
func (f *fakeSession) HTML(context.Context) (string, error) { return f.htmlOut, nil }
func (f *fakeSession) Text(context.Context) (string, error) { return f.textOut, nil }
func (f *fakeSession) Title(context.Context) (string, error) { return f.titleOut, nil }
func (f *fakeSession) Close() error                         { f.closed = true; return nil }

// fakeManager is a Manager backed by a single fake session.
type fakeManager struct {
	session  *fakeSession
	closed   bool
	failNext bool
}

func (m *fakeManager) NewSession() (string, Session, error) {
	if m.failNext {
		return "", nil, errors.New("boom")
	}
	return "s1", m.session, nil
}
func (m *fakeManager) GetSession(id string) (Session, error) {
	if id == "s1" {
		return m.session, nil
	}
	return nil, ErrSessionNotFound
}
func (m *fakeManager) CloseAll() error { m.closed = true; return nil }

func TestNoBrowserManager_FailsClosed(t *testing.T) {
	m := NewNoBrowserManager()
	if _, _, err := m.NewSession(); !errors.Is(err, ErrNoBrowser) {
		t.Fatalf("NewSession: want ErrNoBrowser, got %v", err)
	}
	if _, err := m.GetSession("x"); !errors.Is(err, ErrNoBrowser) {
		t.Fatalf("GetSession: want ErrNoBrowser, got %v", err)
	}
	if err := m.CloseAll(); err != nil {
		t.Fatalf("CloseAll: %v", err)
	}
}

func TestSession_EvaluateGuardEnforced(t *testing.T) {
	f := &fakeSession{}
	if _, err := f.Evaluate(context.Background(), "el.dispatchEvent(new Event('click'))"); !errors.Is(err, ErrSyntheticInput) {
		t.Fatalf("synthetic evaluate must return ErrSyntheticInput, got %v", err)
	}
	out, err := f.Evaluate(context.Background(), "document.title")
	if err != nil {
		t.Fatalf("safe evaluate should not error: %v", err)
	}
	_ = out // value is empty here; the white-box tool test checks content
}

func TestManager_InterfaceLifecycle(t *testing.T) {
	f := &fakeSession{}
	m := &fakeManager{session: f}
	id, s, err := m.NewSession()
	if err != nil || id != "s1" || s == nil {
		t.Fatalf("NewSession: id=%q s=%v err=%v", id, s, err)
	}
	got, err := m.GetSession("s1")
	if err != nil || got == nil {
		t.Fatalf("GetSession: %v", err)
	}
	if _, err := m.GetSession("nope"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("unknown session should be ErrSessionNotFound, got %v", err)
	}
	if err := m.CloseAll(); err != nil {
		t.Fatalf("CloseAll: %v", err)
	}
}
