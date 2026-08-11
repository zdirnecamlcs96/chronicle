package docmodel

import "encoding/json"

// Normalize JSON-normalizes v (marshal then unmarshal into any), so structs
// (honouring json tags) and maps are compared uniformly.
func Normalize(v any) (any, error) {
	bs, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(bs, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CanonJSON canonical-encodes v; an absent value is signalled by the caller
// using "" (this returns "null" for a present JSON null). json.Marshal sorts
// map keys, so output is deterministic. This is the encoding Change.From/To
// hold.
func CanonJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// IsJSONScalar reports whether raw is a single valid JSON value that is not a
// container — the form a chronicleschema ValueType.Canon must return.
func IsJSONScalar(raw string) bool {
	return json.Valid([]byte(raw)) && !IsContainer(ParseJSON(raw))
}

// ParseJSON decodes a Change.From/To value; a malformed encoding yields nil
// rather than an error, because the read side degrades to no metadata.
func ParseJSON(s string) any {
	var v any
	json.Unmarshal([]byte(s), &v) //nolint:errcheck // malformed To just yields no metadata
	return v
}

// ElemKeyValue walks keyPath through nested objects only (an identity path
// never crosses an array).
func ElemKeyValue(el map[string]any, keyPath []string) (any, bool) {
	var cur any = el
	for _, seg := range keyPath {
		obj, isObj := cur.(map[string]any)
		if !isObj {
			return nil, false
		}
		var ok bool
		if cur, ok = obj[seg]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// UsableKey reports whether every element of side is an object holding a
// unique scalar at keyPath; an empty side passes vacuously. Exported only so
// chronicleschema's Config can reach it — the package is internal, so this
// still never reaches a caller.
func UsableKey(keyPath []string, side []any) bool {
	seen := make(map[string]struct{}, len(side))
	for _, el := range side {
		obj, isObj := el.(map[string]any)
		if !isObj {
			return false
		}
		v, ok := ElemKeyValue(obj, keyPath)
		if !ok || v == nil || IsContainer(v) {
			return false
		}
		k := CanonJSON(v)
		if _, dup := seen[k]; dup {
			return false
		}
		seen[k] = struct{}{}
	}
	return true
}
