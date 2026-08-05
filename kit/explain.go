package chroniclekit

import (
	"fmt"

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
type Explained struct {
	changelog.Change
	Field   []string
	Element *Element
	Display *Display
}

// Explain replays commits (the chain from root, OLDEST first — same contract
// as Reconstruct) and decorates every change for display. Because metadata is
// derived from the replayed revisions, records written long before any schema
// was declared decorate exactly like new ones; nothing is stored.
//
// Options declare the caller's schema: WithArrayKeys/the id convention give
// array elements identity, WithLabels supplies i18n labels (Title Case
// fallback), WithNameFields picks the element display-name field.
//
// Result rows align 1:1 with commits and their Changes.
func Explain(commits []changelog.Commit, opts ...DiffOption) ([][]Explained, error) {
	cfg := newDiffConfig(opts)
	out := make([][]Explained, len(commits))
	var cur any = map[string]any{}
	for i, c := range commits {
		rows := make([]Explained, len(c.Changes))
		for j, ch := range c.Changes {
			rows[j] = decorate(&cfg, cur, ch)
			next, err := applyChange(cur, ch)
			if err != nil {
				return nil, fmt.Errorf("explain %s %q: %w", ch.Kind, ch.Path, err)
			}
			cur = next
		}
		out[i] = rows
	}
	return out, nil
}

// decorate derives one change's display metadata by walking its path through
// the pre-change document state, classifying each segment by the container it
// actually traverses (array index vs object key — no guessing).
func decorate(cfg *diffConfig, state any, ch changelog.Change) Explained {
	ex := Explained{Change: ch}
	segs := splitPath(ch.Path)
	if len(segs) == 0 {
		return ex
	}
	var trail, schema []string
	cur := state
	for _, seg := range segs {
		idx, isNum := asIndex(seg)
		_, isArr := cur.([]any)
		// Mirror setIn's vivification rule: numeric segments address arrays,
		// existing maps keep numeric keys as object keys.
		if isNum && (isArr || cur == nil) {
			trail = append(trail, seg)
			cur = elemAt(cur, idx)
			continue
		}
		schema = append(schema, seg)
		trail = append(trail, labelFor(cfg, schema))
		if m, isMap := cur.(map[string]any); isMap {
			cur = m[seg]
		} else {
			cur = nil
		}
	}
	ex.Field = trail
	return ex
}

func elemAt(v any, idx int) any {
	arr, ok := v.([]any)
	if !ok || idx < 0 || idx >= len(arr) {
		return nil
	}
	return arr[idx]
}
