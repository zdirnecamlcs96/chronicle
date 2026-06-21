package chroniclekit

import (
	"encoding/json"
	"strings"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
)

// Operation is one RFC 6902 JSON Patch operation. Path is an RFC 6901 JSON
// Pointer ("/items/0/qty"). Only add/replace/remove are produced/consumed —
// move/copy/test are out of scope for v1.
type Operation struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value,omitempty"`
}

// FromChanges renders changes as a JSON Patch a client can apply. It is
// forward-only: From (the old value) has no place in RFC 6902 and is dropped —
// lossless with respect to applying the patch.
func FromChanges(cs []changelog.Change) []Operation {
	ops := make([]Operation, 0, len(cs))
	for _, c := range cs {
		op := Operation{Op: opForKind(c.Kind), Path: toPointer(c.Path)}
		if c.Kind != KindDelete {
			// RFC 6902 requires a value member for add/replace; default to null
			// when the change carries none.
			if c.To != "" {
				op.Value = json.RawMessage(c.To)
			} else {
				op.Value = json.RawMessage("null")
			}
		}
		ops = append(ops, op)
	}
	return ops
}

// ToChanges ingests a JSON Patch as Changes. Because a patch is forward-only,
// From is left "" (unknown); recover it by diffing against the prior state if you
// need before-values. add/replace/remove are supported; other ops are skipped.
func ToChanges(ops []Operation) []changelog.Change {
	out := make([]changelog.Change, 0, len(ops))
	for _, op := range ops {
		kind, ok := kindForOp(op.Op)
		if !ok {
			continue // move/copy/test: skipped in v1
		}
		c := changelog.Change{Path: fromPointer(op.Path), Kind: kind}
		if kind != KindDelete {
			if len(op.Value) > 0 {
				c.To = string(op.Value)
			} else {
				c.To = "null" // add/replace with no value → null (symmetric with FromChanges)
			}
		}
		out = append(out, c)
	}
	return out
}

func opForKind(kind string) string {
	switch kind {
	case KindCreate:
		return "add"
	case KindDelete:
		return "remove"
	default:
		return "replace"
	}
}

func kindForOp(op string) (string, bool) {
	switch op {
	case "add":
		return KindCreate, true
	case "replace":
		return KindPut, true
	case "remove":
		return KindDelete, true
	default:
		return "", false
	}
}

// toPointer converts a kit dotted path to an RFC 6901 JSON Pointer, escaping
// "~"→"~0" and "/"→"~1" per segment.
func toPointer(p string) string {
	segs := splitPath(p)
	if len(segs) == 0 {
		return ""
	}
	for i, s := range segs {
		s = strings.ReplaceAll(s, "~", "~0")
		s = strings.ReplaceAll(s, "/", "~1")
		segs[i] = s
	}
	return "/" + strings.Join(segs, "/")
}

// fromPointer converts an RFC 6901 JSON Pointer to a kit dotted path, unescaping
// "~1"→"/" then "~0"→"~" (order matters per RFC 6901).
//
// Limitation: the kit's dotted grammar cannot represent an object key that itself
// contains "." (it would split into two segments), nor the empty-string key. Such
// keys do not round-trip; avoid them, or address those documents by Change.Path
// directly rather than via JSON Patch.
func fromPointer(p string) string {
	if p == "" {
		return ""
	}
	p = strings.TrimPrefix(p, "/")
	segs := strings.Split(p, "/")
	for i, s := range segs {
		s = strings.ReplaceAll(s, "~1", "/")
		s = strings.ReplaceAll(s, "~0", "~")
		segs[i] = s
	}
	return strings.Join(segs, ".")
}
