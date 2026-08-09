// Package docmodel is the document plumbing the kit's read and write sides
// share: the dotted path grammar that addresses a value, JSON canonicalization,
// and the Apply that replays a change onto a document.
//
// It is internal on purpose. These are the mechanics chroniclediff,
// chronicleexplain, and chronicleview need from each other, not something a
// caller declares — chronicleschema stays the public vocabulary, and holds
// only what a caller sets. It depends on chronicle/core plus the standard
// library and nothing else of the kit's.
package docmodel

import (
	"strconv"
	"strings"
)

// Path grammar: dotted segments, array indices as numeric segments —
// "items.0.qty" addresses items[0].qty. Backslash escapes a literal dot inside
// a segment (`a\.b` is the single key "a.b") and itself (`\\` is one
// backslash); a backslash before any other byte stays literal, so legacy paths
// without escapes parse unchanged. (chroniclekit's jsonpatch.go maps it to and
// from RFC 6901 JSON Pointer for interop.)

// SplitPath splits a dotted path into segments, honoring backslash escapes;
// "" yields no segments (the root).
func SplitPath(p string) []string {
	if p == "" {
		return nil
	}
	segs := make([]string, 0, 4)
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		switch {
		case p[i] == '\\' && i+1 < len(p) && (p[i+1] == '.' || p[i+1] == '\\'):
			b.WriteByte(p[i+1])
			i++
		case p[i] == '.':
			segs = append(segs, b.String())
			b.Reset()
		default:
			b.WriteByte(p[i])
		}
	}
	return append(segs, b.String())
}

// JoinPath joins segments back into a dotted path, escaping "." and "\" so
// SplitPath(JoinPath(segs)) always round-trips.
func JoinPath(segs []string) string {
	esc := make([]string, len(segs))
	for i, s := range segs {
		s = strings.ReplaceAll(s, `\`, `\\`)
		esc[i] = strings.ReplaceAll(s, ".", `\.`)
	}
	return strings.Join(esc, ".")
}

// ParentPath drops the last segment ("" for a top-level or empty path).
func ParentPath(p string) string {
	segs := SplitPath(p)
	if len(segs) <= 1 {
		return ""
	}
	return JoinPath(segs[:len(segs)-1])
}

// maxIndex caps how large an array index a path segment may vivify. setIn grows
// an []any to idx entries (each ~16 bytes), so an unbounded idx from untrusted
// input is a memory-exhaustion DoS: one 40-byte change with path "x.1048575"
// would allocate ~16MB. Segments above the cap are not treated as array indices
// (they fall through to object keys), so it fails closed without a crash.
// 1<<16 = 65,536 covers any realistic document array while capping one change's
// growth to ~1MB.
// ponytail: per-index cap only. Raise if real arrays exceed 65k, lower for a
// tighter bound. Aggregate growth across many changes is bounded by the httpapi
// body limit (maxBody), not here.
const maxIndex = 1 << 16

// AsIndex reports whether seg is a non-negative array index within the kit's
// vivification cap.
func AsIndex(seg string) (int, bool) {
	n, err := strconv.Atoi(seg)
	if err != nil || n < 0 || n > maxIndex {
		return 0, false
	}
	return n, true
}

// IsContainer reports whether v is an object or array (vs a scalar/leaf).
func IsContainer(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}

// ChildPath returns a fresh slice path+[seg] (never aliases the parent's array).
func ChildPath(path []string, seg string) []string {
	c := make([]string, len(path)+1)
	copy(c, path)
	c[len(path)] = seg
	return c
}
