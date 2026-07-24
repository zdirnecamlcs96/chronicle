// Package chroniclekit is the batteries-included layer over chronicle/core: it
// produces Changes from before/after states (Diff), converts to/from RFC 6902
// JSON Patch, seals via the Service facade, and reconstructs/renders document
// state on read (including a per-commit LCA snapshot). It imports core; core
// never imports it. It is adapter-agnostic — construct it over any
// changelog.Service.
package chroniclekit

import (
	"strconv"
	"strings"
)

// Path grammar (kit-internal): dotted segments, array indices as numeric
// segments — "items.0.qty" addresses items[0].qty. Backslash escapes a literal
// dot inside a segment (`a\.b` is the single key "a.b") and itself (`\\` is one
// backslash); a backslash before any other byte stays literal, so legacy paths
// without escapes parse unchanged. core's Change.Path stays an opaque string;
// this grammar lives only in the kit. (jsonpatch.go maps it to and from RFC
// 6901 JSON Pointer for interop.)

// splitPath splits a dotted path into segments, honoring backslash escapes;
// "" yields no segments (the root).
func splitPath(p string) []string {
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

// joinPath joins segments back into a dotted path, escaping "." and "\" so
// splitPath(joinPath(segs)) always round-trips.
func joinPath(segs []string) string {
	esc := make([]string, len(segs))
	for i, s := range segs {
		s = strings.ReplaceAll(s, `\`, `\\`)
		esc[i] = strings.ReplaceAll(s, ".", `\.`)
	}
	return strings.Join(esc, ".")
}

// parentPath drops the last segment ("" for a top-level or empty path).
func parentPath(p string) string {
	segs := splitPath(p)
	if len(segs) <= 1 {
		return ""
	}
	return joinPath(segs[:len(segs)-1])
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

// asIndex reports whether seg is a non-negative array index within maxIndex.
func asIndex(seg string) (int, bool) {
	n, err := strconv.Atoi(seg)
	if err != nil || n < 0 || n > maxIndex {
		return 0, false
	}
	return n, true
}

// isContainer reports whether v is an object or array (vs a scalar/leaf).
func isContainer(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}
