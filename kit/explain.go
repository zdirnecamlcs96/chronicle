package chroniclekit

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
)

// Element identifies the keyed array element a change sits inside.
type Element struct {
	Trail []string // label trail of the containing array, from the document root
	Name  string   // element display name
	ID    string   // element identity value, human form
}

// Display carries resolved display names for id-valued From/To values.
type Display struct {
	From string
	To   string
}

// Explained decorates one recorded Change with display metadata derived at
// read time from the replayed revision context. The embedded Change is the
// stored record, untouched.
//
//   - Field   the human label trail of the changed field — relative to the
//     element root when Element is set, else to the document root.
//   - Element set when the change sits inside a keyed array element; a
//     whole-element addition/removal is Element with an empty Field.
//   - Display resolved names when From/To values are known ids.
//   - Bookkeeping true when the change touches a WithIgnoredFields entry (a
//     bare name at any depth, or a schema path and its subtree) — recorded
//     data a human display would fold away.
//   - FromValue/ToValue a container From/To decomposed as a ValueNode tree —
//     everything a display needs to break the value down per field without
//     re-implementing the schema walk. Nil when the side is scalar or absent.
type Explained struct {
	changelog.Change
	Field       []string
	Element     *Element
	Display     *Display
	Bookkeeping bool
	FromValue   *ValueNode
	ToValue     *ValueNode
}

// ValueNode is a container value decomposed for display under the caller's
// schema. It carries structure only — labels, element identity, canonical
// scalars, order; joining, truncation, counts, and styling are the
// consumer's business, so the kit never limits how it renders.
//
//   - Label  the node's display label: a field's label (WithLabels, Title
//     Case fallback), an array element's display name (name fields / id→name
//     index, index when identity is unusable), or — on the root — the
//     value's own display name when it has one ("" otherwise).
//   - Value  the canonical-JSON scalar at a leaf; "" on containers.
//   - List   true when the node is an array (kids are elements, in order);
//     false when it is an object (kids are fields, sorted by name).
//   - Bookkeeping  true when the node's field is a WithIgnoredFields entry —
//     the same mark rows carry, so displays fold noise inside the tree too.
//   - Kids   the container's children; nil at leaves.
type ValueNode struct {
	Label       string
	Value       string
	List        bool
	Bookkeeping bool
	Kids        []ValueNode
}

// Explain replays commits (the chain from root, OLDEST first — same contract
// as Reconstruct) and decorates every change for display. Because metadata is
// derived from the replayed revisions, records written long before any schema
// was declared decorate exactly like new ones; nothing is stored.
//
// Options declare the caller's schema: WithArrayKeys/the id convention give
// array elements identity, WithLabels supplies i18n labels (Title Case
// fallback), WithNameFields picks the element display-name field, and
// WithIgnoredFields flags bookkeeping changes (Bookkeeping) for displays to
// fold away — the stored record always keeps them. Display names come from
// id→name pairs found in either revision surrounding each commit (the after
// revision wins conflicts).
//
// Result rows align 1:1 with commits and their Changes.
//
// ponytail: each commit costs one extra deep copy + two name walks, O(doc);
// derive incrementally if huge chains ever make rendering slow.
func Explain(commits []changelog.Commit, opts ...DiffOption) ([][]Explained, error) {
	cfg := newDiffConfig(opts)
	out := make([][]Explained, len(commits))
	var cur any // nil root so the first change vivifies object or array docs alike
	for i, c := range commits {
		names := map[string]string{}
		walkNames(&cfg, cur, nil, names)
		after, err := normalize(cur) // deep copy
		if err != nil {
			return nil, err
		}
		for _, ch := range c.Changes {
			if after, err = applyChange(after, ch); err != nil {
				return nil, fmt.Errorf("explain %s %q: %w", ch.Kind, ch.Path, err)
			}
		}
		walkNames(&cfg, after, nil, names) // after revision wins conflicts

		rows := make([]Explained, len(c.Changes))
		for j, ch := range c.Changes {
			rows[j] = decorate(&cfg, cur, ch, names)
			if cur, err = applyChange(cur, ch); err != nil {
				return nil, fmt.Errorf("explain %s %q: %w", ch.Kind, ch.Path, err)
			}
		}
		out[i] = rows
	}
	return out, nil
}

// decorate derives one change's display metadata by walking its path through
// the pre-change document state, classifying each segment by the container it
// actually traverses (array index vs object key — no guessing).
func decorate(cfg *diffConfig, state any, ch changelog.Change, names map[string]string) Explained {
	ex := Explained{Change: ch, Display: displayFor(names, ch)}
	segs := splitPath(ch.Path)
	if len(segs) == 0 {
		return ex
	}
	var trail, schema []string
	var elem *Element
	fieldStart := 0
	cur := state
	for si, seg := range segs {
		idx, isNum := asIndex(seg)
		arr, isArr := cur.([]any)
		// Mirror setIn's vivification rule: numeric segments address arrays,
		// existing maps keep numeric keys as object keys.
		if isNum && (isArr || cur == nil) {
			elVal := elemAt(cur, idx)
			if elVal == nil && si == len(segs)-1 && ch.Kind == KindCreate {
				elVal = parseJSON(ch.To) // incoming element, not in pre-change state
			}
			if obj, isObj := elVal.(map[string]any); isObj {
				if kp, ok := readKey(cfg, schema, arr); ok {
					if kv, kok := elemKeyValue(obj, kp); kok && kv != nil && !isContainer(kv) {
						elem = &Element{ // innermost keyed element wins
							Trail: append([]string(nil), trail...),
							ID:    humanScalar(kv),
							Name:  resolveName(cfg, obj, kv, names),
						}
						fieldStart = len(trail)
						cur = elVal
						continue // identity replaces the index in the trail
					}
				}
			}
			trail = append(trail, seg) // identity-less array: index verbatim
			cur = elVal
			continue
		}
		schema = append(schema, seg)
		trail = append(trail, labelFor(cfg, schema))
		if bookkeeping(cfg, seg, schema) {
			ex.Bookkeeping = true
		}
		if m, isMap := cur.(map[string]any); isMap {
			cur = m[seg]
		} else {
			cur = nil
		}
	}
	if field := trail[fieldStart:]; len(field) > 0 {
		ex.Field = field
	}
	ex.Element = elem
	ex.FromValue = containerValue(cfg, schema, ch.From, names)
	ex.ToValue = containerValue(cfg, schema, ch.To, names)
	return ex
}

// containerValue decomposes a container From/To into its ValueNode tree; nil
// for scalars and absent values. schema is the change's index-free path, so
// nested arrays inside the value resolve their WithArrayKeys identity.
func containerValue(cfg *diffConfig, schema []string, raw string, names map[string]string) *ValueNode {
	v := parseJSON(raw)
	if !isContainer(v) {
		return nil
	}
	label := ""
	if obj, isObj := v.(map[string]any); isObj {
		for _, nf := range cfg.nameFields {
			if s, ok := obj[nf].(string); ok && s != "" {
				label = s
				break
			}
		}
	}
	n := valueNode(cfg, schema, label, v, names)
	return &n
}

func valueNode(cfg *diffConfig, schema []string, label string, v any, names map[string]string) ValueNode {
	n := ValueNode{Label: label}
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			cs := childPath(schema, k)
			kid := valueNode(cfg, cs, labelFor(cfg, cs), t[k], names)
			kid.Bookkeeping = bookkeeping(cfg, k, cs)
			n.Kids = append(n.Kids, kid)
		}
	case []any:
		n.List = true
		kp, keyed := readKey(cfg, schema, t)
		for i, el := range t {
			lbl := strconv.Itoa(i)
			if obj, isObj := el.(map[string]any); isObj && keyed {
				if kv, ok := elemKeyValue(obj, kp); ok && kv != nil && !isContainer(kv) {
					lbl = resolveName(cfg, obj, kv, names)
				}
			}
			// schema passes through array elements unchanged — index-free.
			n.Kids = append(n.Kids, valueNode(cfg, schema, lbl, el, names))
		}
	default:
		n.Value = mustJSON(v)
	}
	return n
}

// bookkeeping reports whether the field at schema (whose final segment is
// seg) is a WithIgnoredFields entry: its bare name, or its schema path.
// Subtree coverage needs no prefix walk here — decorate checks every segment
// as it descends, and valueNode marks the ancestor node itself.
func bookkeeping(cfg *diffConfig, seg string, schema []string) bool {
	if _, ok := cfg.ignoredNames[seg]; ok {
		return true
	}
	_, ok := cfg.ignoredPaths[joinPath(schema)]
	return ok
}

// readKey is the read-time identity chain for the array at schema: the
// caller-configured key, else the "id" convention — usable when the elements
// present hold unique scalars at the key path (an absent/empty array passes
// vacuously; the element under decoration still decides for itself).
func readKey(cfg *diffConfig, schema []string, side []any) ([]string, bool) {
	if p, ok := cfg.arrayKeys[joinPath(schema)]; ok {
		kp := splitPath(p)
		if usableKey(kp, side) {
			return kp, true
		}
	}
	kp := []string{"id"}
	if usableKey(kp, side) {
		return kp, true
	}
	return nil, false
}

// resolveName picks an element's display name: its own first non-empty
// name field, else the id→name index, else the id itself.
func resolveName(cfg *diffConfig, obj map[string]any, keyValue any, names map[string]string) string {
	for _, nf := range cfg.nameFields {
		if s, ok := obj[nf].(string); ok && s != "" {
			return s
		}
	}
	if n, ok := names[mustJSON(keyValue)]; ok {
		return n
	}
	return humanScalar(keyValue)
}

// walkNames indexes id→name pairs found anywhere in doc: any object holding a
// scalar "id" plus a non-empty name field, and elements of configured keyed
// arrays via their declared key.
func walkNames(cfg *diffConfig, doc any, schema []string, names map[string]string) {
	switch v := doc.(type) {
	case map[string]any:
		indexNames(cfg, v, []string{"id"}, names)
		for k, child := range v {
			walkNames(cfg, child, childPath(schema, k), names)
		}
	case []any:
		var kp []string
		if p, ok := cfg.arrayKeys[joinPath(schema)]; ok {
			kp = splitPath(p)
		}
		for _, el := range v {
			if obj, isObj := el.(map[string]any); isObj && kp != nil {
				indexNames(cfg, obj, kp, names)
			}
			walkNames(cfg, el, schema, names)
		}
	}
}

func indexNames(cfg *diffConfig, obj map[string]any, keyPath []string, names map[string]string) {
	kv, ok := elemKeyValue(obj, keyPath)
	if !ok || kv == nil || isContainer(kv) {
		return
	}
	for _, nf := range cfg.nameFields {
		if s, sok := obj[nf].(string); sok && s != "" {
			names[mustJSON(kv)] = s
			return
		}
	}
}

func displayFor(names map[string]string, ch changelog.Change) *Display {
	from, fok := names[ch.From]
	to, tok := names[ch.To]
	if !fok && !tok {
		return nil
	}
	return &Display{From: from, To: to}
}

func humanScalar(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return mustJSON(v)
}

func parseJSON(s string) any {
	var v any
	json.Unmarshal([]byte(s), &v) //nolint:errcheck // malformed To just yields no metadata
	return v
}

func elemAt(v any, idx int) any {
	arr, ok := v.([]any)
	if !ok || idx < 0 || idx >= len(arr) {
		return nil
	}
	return arr[idx]
}
