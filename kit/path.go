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
// segments — "items.0.qty" addresses items[0].qty. core's Change.Path stays an
// opaque string; this grammar lives only in the kit. (jsonpatch.go maps it to
// and from RFC 6901 JSON Pointer for interop.)

// splitPath splits a dotted path into segments; "" yields no segments (the root).
func splitPath(p string) []string {
	if p == "" {
		return nil
	}
	return strings.Split(p, ".")
}

// joinPath joins segments back into a dotted path.
func joinPath(segs []string) string {
	return strings.Join(segs, ".")
}

// parentPath drops the last segment ("" for a top-level or empty path).
func parentPath(p string) string {
	segs := splitPath(p)
	if len(segs) <= 1 {
		return ""
	}
	return joinPath(segs[:len(segs)-1])
}

// asIndex reports whether seg is a non-negative array index.
func asIndex(seg string) (int, bool) {
	n, err := strconv.Atoi(seg)
	if err != nil || n < 0 {
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
