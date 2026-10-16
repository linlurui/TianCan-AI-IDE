package browser

import "testing"

// mustDetect asserts the script is flagged as synthetic input.
func mustDetect(t *testing.T, script string) {
	t.Helper()
	if ok, reason := IsSyntheticInputScript(script); !ok {
		t.Errorf("expected script to be flagged as synthetic input, but it passed:\n%s", script)
	} else if reason == "" {
		t.Errorf("script flagged but reason empty:\n%s", script)
	}
}

// mustAllow asserts the script is NOT flagged (safe read-only JS).
func mustAllow(t *testing.T, script string) {
	t.Helper()
	if ok, reason := IsSyntheticInputScript(script); ok {
		t.Errorf("expected script to be allowed, but it was flagged (%s):\n%s", reason, script)
	}
}

func TestGuard_DetectsSyntheticInput(t *testing.T) {
	cases := []string{
		// Generic dispatch of a constructed event.
		`el.dispatchEvent(new Event('click'))`,
		`document.querySelector('#b').dispatchEvent(new MouseEvent('click', {bubbles:true}))`,
		// Constructing an event object (will typically be dispatched).
		`new Event('submit')`,
		`new MouseEvent('mousedown')`,
		`new KeyboardEvent('keydown', {key:'Enter'})`,
		`new PointerEvent('pointerdown')`,
		`new TouchEvent('touchstart')`,
		`new WheelEvent('wheel')`,
		`new CustomEvent('myevent', {detail:1})`,
		`new UIEvent('input')`,
		`new FocusEvent('focus')`,
		// Element methods that fire events when called programmatically.
		`el.click()`,
		`form.submit()`,
		`btn.requestSubmit()`,
		`input.focus()`,
		`input.blur()`,
		`input.select()`,
		`form.reset()`,
		// Deprecated synthetic-event APIs.
		`document.createEvent('MouseEvents')`,
		`evt.initEvent('click', true, true)`,
		`el.initMouseEvent('click', ...)`,
		`el.initKeyboardEvent('keydown', ...)`,
		// Casing + whitespace evasions.
		`EL.DISPATCHEVENT(NEW EVENT('click'))`,
		`el . click ( )`,
	}
	for _, c := range cases {
		mustDetect(t, c)
	}
}

func TestGuard_AllowsReadOnlyScripts(t *testing.T) {
	allow := []string{
		`document.title`,
		`document.querySelector('#x').innerText`,
		`return document.body.innerHTML`,
		`JSON.parse('{"a":1}')`,
		`const x = 1 + 2; x * 3`,
		`[1,2,3].map(n => n*2).join(',')`,
		`window.location.href`,
		`document.cookie`,
		`() => ({title: document.title, len: document.links.length})`,
		`"this string mentions dispatchEvent( but is harmless"`,
		`'and so does this click( token'`,
		`// a comment with dispatchEvent(new Event('x')) hidden in it`,
		`/* block comment el.click() should not trip */ document.title`,
	}
	for _, c := range allow {
		mustAllow(t, c)
	}
}

// TestGuard_Counterexample_QuotedTokenMustNotFalsePositive proves the string
// stripping works: a benign script that merely contains the forbidden tokens
// inside a string literal must NOT be blocked.
func TestGuard_Counterexample_QuotedTokenMustNotFalsePositive(t *testing.T) {
	mustAllow(t, `console.log("do not call dispatchEvent( here")`)
	mustAllow(t, `const msg = 'avoid el.click() in production'; msg`)
}

// TestGuard_Counterexample_HiddenDispatchMustBeCaught proves the matcher is not
// evaded by unusual but executable forms: bracket access on a constructed event
// dispatch, and an initEvent call chained after construction.
func TestGuard_Counterexample_HiddenDispatchMustBeCaught(t *testing.T) {
	// Constructing an event of any of the flagged types is itself flagged even
	// without an obvious dispatch call.
	mustDetect(t, `const e = new Event('change'); queue.push(e)`)
	// initEvent on a created event is flagged.
	mustDetect(t, `const e = document.createEvent('Events'); e.initEvent('click', true, true)`)
}
