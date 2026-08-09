// Package chronicleexplain is the kit's display side: it replays a chain of
// commits and decorates every recorded Change with the metadata a human
// display needs — label trails, keyed-element identity and names, bookkeeping
// flags, and container values broken down as ValueNode trees.
//
// Everything here is derived at READ time from the replayed revisions, under
// the schema the caller declares on the call. Nothing display-shaped is ever
// stored, so records written long before any schema existed decorate exactly
// like new ones, and changing the schema changes only how history reads —
// never what it says.
package chronicleexplain

import (
	"fmt"
	"maps"
	"sort"
	"strconv"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
	"github.com/zdirnecamlcs96/chronicle/kit/internal/docmodel"
	chronicleschema "github.com/zdirnecamlcs96/chronicle/kit/schema"
)

// Element identifies the keyed array element a change sits inside.
type Element struct {
	Trail []string `json:"trail,omitempty"` // label trail of the containing array, from the document root
	Name  string   `json:"name,omitempty"`  // element display name
	ID    string   `json:"id,omitempty"`    // element identity value, human form
}

// Display carries resolved display names for id-valued From/To values.
type Display struct {
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
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
//
// The json tags are snake_case so a row serializes consistently with the rest
// of a JSON API. The embedded Change inlines, keeping Path — the raw dotted
// path — on every row: it is the stable key a client translates from when it
// wants its own i18n labels rather than the Title Case fallback in Field.
type Explained struct {
	changelog.Change
	Field       []string   `json:"field,omitempty"`
	Element     *Element   `json:"element,omitempty"`
	Display     *Display   `json:"display,omitempty"`
	Bookkeeping bool       `json:"bookkeeping,omitempty"`
	FromValue   *ValueNode `json:"from_value,omitempty"`
	ToValue     *ValueNode `json:"to_value,omitempty"`
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
//   - Display the resolved name when Value is a known id — a name found in the
//     replayed revisions or a WithNames pair; "" otherwise. Value stays the
//     record and is never overwritten, so a display can show the name and keep
//     the id.
//   - List   true when the node is an array (kids are elements, in order);
//     false when it is an object (kids are fields, sorted by name).
//   - Bookkeeping  true when the node's field is a WithIgnoredFields entry —
//     the same mark rows carry, so displays fold noise inside the tree too.
//   - Kids   the container's children; nil at leaves.
type ValueNode struct {
	Label       string      `json:"label,omitempty"`
	Value       string      `json:"value,omitempty"`
	Display     string      `json:"display,omitempty"`
	List        bool        `json:"list,omitempty"`
	Bookkeeping bool        `json:"bookkeeping,omitempty"`
	Kids        []ValueNode `json:"kids,omitempty"`
}

// Explain replays commits (the chain from root, OLDEST first — same contract
// as Reconstruct) and decorates every change for display. Because metadata is
// derived from the replayed revisions, records written long before any schema
// was declared decorate exactly like new ones; nothing is stored.
//
// Options declare the caller's schema: WithArrayKeys/WithIdentityFields give
// array elements identity, WithLabels supplies i18n labels (Title Case
// fallback), WithNameFields picks the element display-name field, and
// WithIgnoredFields flags bookkeeping changes (Bookkeeping) for displays to
// fold away — the stored record always keeps them. Display names come from
// id→name pairs found in either revision surrounding each commit (the after
// revision wins conflicts), over any WithNames dictionary the caller supplies
// for ids whose entities live outside the document.
//
// Result rows align 1:1 with commits and their Changes.
//
// ponytail: each commit costs one extra deep copy + two name walks, O(doc);
// derive incrementally if huge chains ever make rendering slow.
func Explain(commits []changelog.Commit, opts ...chronicleschema.Option) ([][]Explained, error) {
	cfg := chronicleschema.New(opts...)
	out := make([][]Explained, len(commits))
	var cur any // nil root so the first change vivifies object or array docs alike
	for i, c := range commits {
		names := map[string]string{}
		maps.Copy(names, cfg.Names()) // WithNames seeds; the document overwrites
		walkNames(&cfg, cur, nil, names)
		after, err := docmodel.Normalize(cur) // deep copy
		if err != nil {
			return nil, err
		}
		for _, ch := range c.Changes {
			if after, err = docmodel.Apply(after, ch); err != nil {
				return nil, fmt.Errorf("explain %s %q: %w", ch.Kind, ch.Path, err)
			}
		}
		walkNames(&cfg, after, nil, names) // after revision wins conflicts

		rows := make([]Explained, len(c.Changes))
		for j, ch := range c.Changes {
			rows[j] = decorate(&cfg, cur, ch, names)
			if cur, err = docmodel.Apply(cur, ch); err != nil {
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
func decorate(cfg *chronicleschema.Config, state any, ch changelog.Change, names map[string]string) Explained {
	ex := Explained{Change: ch, Display: displayFor(names, ch)}
	segs := docmodel.SplitPath(ch.Path)
	if len(segs) == 0 {
		return ex
	}
	var trail, schema []string
	var elem *Element
	fieldStart := 0
	cur := state
	for si, seg := range segs {
		idx, isNum := docmodel.AsIndex(seg)
		arr, isArr := cur.([]any)
		// Mirror setIn's vivification rule: numeric segments address arrays,
		// existing maps keep numeric keys as object keys.
		if isNum && (isArr || cur == nil) {
			elVal := elemAt(cur, idx)
			if elVal == nil && si == len(segs)-1 && ch.Kind == chronicleschema.KindCreate {
				elVal = docmodel.ParseJSON(ch.To) // incoming element, not in pre-change state
			}
			if obj, isObj := elVal.(map[string]any); isObj {
				if kp, ok := cfg.IdentityKey(schema, arr); ok {
					if kv, kok := docmodel.ElemKeyValue(obj, kp); kok && kv != nil && !docmodel.IsContainer(kv) {
						elem = &Element{ // innermost keyed element wins
							Trail: append([]string(nil), trail...),
							ID:    humanScalar(kv),
							Name:  resolveName(cfg, obj, kp, kv, names),
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
		trail = append(trail, cfg.Label(schema))
		if cfg.Bookkeeping(seg, schema) {
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
func containerValue(cfg *chronicleschema.Config, schema []string, raw string, names map[string]string) *ValueNode {
	v := docmodel.ParseJSON(raw)
	if !docmodel.IsContainer(v) {
		return nil
	}
	label := ""
	if obj, isObj := v.(map[string]any); isObj {
		label = nameField(cfg, obj)
		// The value may itself be an element of the keyed array at schema — a
		// whole-element add/remove — so its name can live on the object holding
		// its identity, exactly as it does for Element.Name.
		if label == "" {
			if kp, ok := cfg.IdentityKey(schema, []any{v}); ok {
				if idObj := identityObject(obj, kp); idObj != nil {
					label = nameField(cfg, idObj)
				}
			}
		}
	}
	n := valueNode(cfg, schema, label, v, names)
	return &n
}

func valueNode(cfg *chronicleschema.Config, schema []string, label string, v any, names map[string]string) ValueNode {
	n := ValueNode{Label: label}
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			cs := docmodel.ChildPath(schema, k)
			kid := valueNode(cfg, cs, cfg.Label(cs), t[k], names)
			kid.Bookkeeping = cfg.Bookkeeping(k, cs)
			n.Kids = append(n.Kids, kid)
		}
	case []any:
		n.List = true
		kp, keyed := cfg.IdentityKey(schema, t)
		for i, el := range t {
			lbl := strconv.Itoa(i)
			if obj, isObj := el.(map[string]any); isObj && keyed {
				if kv, ok := docmodel.ElemKeyValue(obj, kp); ok && kv != nil && !docmodel.IsContainer(kv) {
					lbl = resolveName(cfg, obj, kp, kv, names)
				}
			}
			// schema passes through array elements unchanged — index-free.
			n.Kids = append(n.Kids, valueNode(cfg, schema, lbl, el, names))
		}
	default:
		n.Value = docmodel.CanonJSON(v)
		n.Display = names[n.Value] // "" unless the leaf is a known id
	}
	return n
}

// nameField returns obj's first non-empty configured name field, "" if none.
func nameField(cfg *chronicleschema.Config, obj map[string]any) string {
	for _, nf := range cfg.NameFields() {
		if s, ok := obj[nf].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// identityObject returns the object a dotted key path descends into — the one
// the identity value belongs to, and so the one that names it. nil for a
// single-segment path, where the element root already is that object.
func identityObject(obj map[string]any, keyPath []string) map[string]any {
	if len(keyPath) < 2 {
		return nil
	}
	v, ok := docmodel.ElemKeyValue(obj, keyPath[:len(keyPath)-1])
	if !ok {
		return nil
	}
	m, _ := v.(map[string]any)
	return m
}

// resolveName picks an element's display name: its own first non-empty name
// field, else — when identity is a dot-path — the name field of the object
// that path descends into, else the id→name index, else the id itself. An
// element may name itself; that is a different question from what the entity
// it carries is called, which is why indexNames does not share this order.
func resolveName(cfg *chronicleschema.Config, obj map[string]any, keyPath []string, keyValue any, names map[string]string) string {
	if s := nameField(cfg, obj); s != "" {
		return s
	}
	if idObj := identityObject(obj, keyPath); idObj != nil {
		if s := nameField(cfg, idObj); s != "" {
			return s
		}
	}
	if n, ok := names[docmodel.CanonJSON(keyValue)]; ok {
		return n
	}
	return humanScalar(keyValue)
}

// walkNames indexes id→name pairs found anywhere in doc: any object holding a
// scalar at a WithIdentityFields entry plus a non-empty name field, and
// elements of configured keyed arrays via their declared key. With no identity
// fields declared the generic sweep does nothing — the kit guesses no names.
func walkNames(cfg *chronicleschema.Config, doc any, schema []string, names map[string]string) {
	switch v := doc.(type) {
	case map[string]any:
		for _, f := range cfg.IdentityFields() {
			indexNames(cfg, v, []string{f}, names)
		}
		for k, child := range v {
			walkNames(cfg, child, docmodel.ChildPath(schema, k), names)
		}
	case []any:
		var kp []string
		if p, ok := cfg.ArrayKey(schema); ok {
			kp = p
		}
		for _, el := range v {
			if obj, isObj := el.(map[string]any); isObj && kp != nil {
				indexNames(cfg, obj, kp, names)
			}
			walkNames(cfg, el, schema, names)
		}
	}
}

// indexNames records obj's id→name pair under keyPath. The name comes from the
// object the identity belongs to — the element root for a single-segment key,
// the object a dot-path descends into otherwise. The index is global, read back
// for any id anywhere (displayFor, ValueNode.Display), so an element's own name
// must never be filed against an id it merely carries.
func indexNames(cfg *chronicleschema.Config, obj map[string]any, keyPath []string, names map[string]string) {
	kv, ok := docmodel.ElemKeyValue(obj, keyPath)
	if !ok || kv == nil || docmodel.IsContainer(kv) {
		return
	}
	owner := obj
	if idObj := identityObject(obj, keyPath); idObj != nil {
		owner = idObj
	}
	if s := nameField(cfg, owner); s != "" {
		names[docmodel.CanonJSON(kv)] = s
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
	return docmodel.CanonJSON(v)
}

func elemAt(v any, idx int) any {
	arr, ok := v.([]any)
	if !ok || idx < 0 || idx >= len(arr) {
		return nil
	}
	return arr[idx]
}
