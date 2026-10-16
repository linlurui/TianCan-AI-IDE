package schema

import (
	"encoding/json"
	"fmt"
)

// Redact returns a NEW map that is a deep copy of in with every secret path
// (per Schema.IsSecret) structurally removed. The input map is never mutated.
//
// It is open-world: paths not declared secret in the schema are preserved
// verbatim, including nested maps and slices.
func (s Schema) Redact(in map[string]interface{}) map[string]interface{} {
	out := deepCopyMap(in)
	s.redactInto(out, "")
	return out
}

// redactInto walks m recursively, deleting any key whose dotted path is secret.
// When a secret path is found, the entire key (and its subtree) is removed.
func (s Schema) redactInto(m map[string]interface{}, prefix string) {
	for k := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if s.IsSecret(path) {
			delete(m, k)
			continue
		}
		if child, ok := m[k].(map[string]interface{}); ok {
			s.redactInto(child, path)
		}
	}
}

// RedactJSON is the UI-boundary primitive: it decodes JSON, structurally removes
// secret fields, and re-encodes. It never mutates the input slice. Decoding uses
// a generic map so nested objects are represented as map[string]interface{} and
// are fully traversable for redaction.
func (s Schema) RedactJSON(in []byte) ([]byte, error) {
	if len(in) == 0 {
		return in, nil
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(in, &decoded); err != nil {
		return nil, fmt.Errorf("schema: decode json for redact: %w", err)
	}
	redacted := s.Redact(decoded)
	out, err := json.Marshal(redacted)
	if err != nil {
		return nil, fmt.Errorf("schema: encode redacted json: %w", err)
	}
	return out, nil
}

// deepCopyMap returns a deep copy of m (maps and slices are copied recursively;
// other values are shared by reference, which is safe for JSON-shaped data).
func deepCopyMap(m map[string]interface{}) map[string]interface{} {
	return deepCopy(m).(map[string]interface{})
}

func deepCopy(v interface{}) interface{} {
	switch x := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, val := range x {
			out[k] = deepCopy(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, val := range x {
			out[i] = deepCopy(val)
		}
		return out
	default:
		return v
	}
}
