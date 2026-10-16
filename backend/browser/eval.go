package browser

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/ysmood/gson"
)

// funcExprRE matches scripts that are already function expressions, e.g.
// "() => ...", "async()=>{...}", "function() {...}", "x => x + 1".
//
// The optional leading "async" allows zero whitespace ("async()=>{}") because
// that exact form is what the provider chat scripts ship; requiring \s+ here
// silently mis-wrapped them into a function *returning* an async function.
var funcExprRE = regexp.MustCompile(
	`^(?:async\s*)?(?:function\b|\(\s*\)\s*=>|\([^)]*\)\s*=>|[A-Za-z_$][\w$]*\s*=>)`)

// WrapFunc adapts a JS script to rod's Eval contract, which always calls the
// script as a function: `<js>.apply(this, arguments)`. A bare expression such as
// `document.title` must therefore be wrapped; a script that is already a
// function expression is passed through unchanged, because wrapping a function
// would return the function object instead of invoking it.
func WrapFunc(expr string) string {
	t := strings.TrimSpace(expr)
	if t == "" {
		return "() => undefined"
	}
	if funcExprRE.MatchString(t) {
		return t
	}
	return "() => (" + t + ")"
}

// RenderValue converts a CDP evaluate result into a stable string.
//
// gson keeps a value as its unparsed JSON bytes until a typed accessor forces a
// parse, so Raw() commonly returns []byte here. Handing that straight to
// json.Marshal would base64-encode it (Go's rule for []byte), turning a result of
// `true` into "dHJ1ZQ=="; we therefore decode it into a real JSON value first.
func RenderValue(v gson.JSON) string {
	raw := v.Raw()
	if raw == nil {
		return ""
	}
	return renderRaw(raw)
}

// DecodeValue converts a CDP evaluate result into a plain Go value
// (map[string]interface{}, []interface{}, string, float64, bool, nil).
// Callers such as the provider chat scripts return JS objects, which arrive here
// as map[string]interface{} and can be inspected directly.
func DecodeValue(v gson.JSON) interface{} {
	raw := v.Raw()
	if raw == nil {
		return nil
	}
	if b, ok := raw.([]byte); ok {
		var decoded interface{}
		if err := json.Unmarshal(b, &decoded); err != nil {
			return string(b)
		}
		return decoded
	}
	return raw
}

func renderRaw(raw interface{}) string {
	if b, ok := raw.([]byte); ok {
		var decoded interface{}
		if err := json.Unmarshal(b, &decoded); err != nil {
			// Not valid JSON: surface the original text rather than failing.
			return string(b)
		}
		raw = decoded
	}
	if s, ok := raw.(string); ok {
		return s
	}
	enc, err := json.Marshal(raw)
	if err != nil {
		return fmt.Sprintf("%v", raw)
	}
	return string(enc)
}
