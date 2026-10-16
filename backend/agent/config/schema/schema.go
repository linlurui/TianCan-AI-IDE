// Package schema defines a declarative settings schema and a secret-redaction
// primitive that structurally removes fields flagged role('secret') before they
// cross the UI boundary.
//
// Design notes (G11):
//   - A Field's Path is a dotted key path, e.g. "env.OPENAI_API_KEY" or the
//     wildcard "env.*" (trailing ".*" means "the subtree rooted here").
//   - Schema.IsSecret is conservative: a path is secret if it equals a declared
//     secret field OR sits under a secret wildcard.
//   - Union merges several branch schemas conservatively: if ANY branch marks a
//     field secret, the union marks it secret ("任一分支声明 secret 就删").
//   - Public returns a copy with every secret field's Default zeroed, so the
//     schema sent to the UI never leaks a default secret value
//     ("默认值也要脱敏").
//
// The package depends only on the standard library, so it can be imported by
// backend/agent/config without creating an import cycle.
package schema

import "strings"

// Role classifies a setting field for the serialization / UI boundary.
type Role string

const (
	// RoleNormal is the zero value: the field may cross the UI boundary as-is.
	RoleNormal Role = ""
	// RoleSecret marks a field whose value must be structurally removed before
	// crossing the UI boundary.
	RoleSecret Role = "secret"
)

// Field declares a single setting field within a Schema.
type Field struct {
	// Path is a dotted key path. A trailing ".*" wildcard matches the whole
	// subtree rooted at the prefix (e.g. "env.*" matches "env" and
	// "env.OPENAI_API_KEY").
	Path string
	// Role is Normal or Secret.
	Role Role
	// Default is the field's default value. It is redacted (zeroed) when the
	// field is secret, even at the schema level (see Public).
	Default interface{}
}

// Schema is an ordered collection of Field declarations.
type Schema struct {
	Fields []Field
}

// IsSecret reports whether path (or a wildcard ancestor of it) is declared
// secret in this schema. Open-world: paths not mentioned in the schema are
// treated as normal and are NOT secret.
func (s Schema) IsSecret(path string) bool {
	for _, f := range s.Fields {
		if f.Role != RoleSecret {
			continue
		}
		if isSecretPath(f.Path, path) {
			return true
		}
	}
	return false
}

// isSecretPath evaluates a single declaration against a concrete path.
// A trailing ".*" on the declaration matches the prefix itself and every
// descendant (so "env.*" removes the entire env subtree — the conservative
// choice for secrets).
func isSecretPath(decl, path string) bool {
	if strings.HasSuffix(decl, ".*") {
		base := decl[:len(decl)-2]
		return path == base || strings.HasPrefix(path, base+".")
	}
	return decl == path
}

// Union merges branch schemas conservatively. For each unique path:
//   - Role is Secret if ANY input schema marks it Secret (secret wins).
//   - Default is taken from the first non-nil occurrence.
//
// The field order follows first-appearance across the inputs.
func Union(schemas ...Schema) Schema {
	type acc struct {
		role Role
		def  interface{}
	}
	m := make(map[string]acc)
	order := make([]string, 0, len(schemas)*4)
	for _, sc := range schemas {
		for _, f := range sc.Fields {
			a, ok := m[f.Path]
			if !ok {
				m[f.Path] = acc{role: f.Role, def: f.Default}
				order = append(order, f.Path)
				continue
			}
			if f.Role == RoleSecret {
				a.role = RoleSecret
			}
			if a.def == nil && f.Default != nil {
				a.def = f.Default
			}
			m[f.Path] = a
		}
	}
	out := Schema{Fields: make([]Field, 0, len(order))}
	for _, p := range order {
		a := m[p]
		out.Fields = append(out.Fields, Field{Path: p, Role: a.role, Default: a.def})
	}
	return out
}

// Public returns a copy of the schema with every secret field's Default zeroed.
// This is the schema variant safe to hand to the UI: it describes the fields
// and their roles but never carries a default secret value.
func (s Schema) Public() Schema {
	out := Schema{Fields: make([]Field, len(s.Fields))}
	for i, f := range s.Fields {
		nf := f
		if f.Role == RoleSecret {
			nf.Default = nil
		}
		out.Fields[i] = nf
	}
	return out
}
