package chronicleexplain

import (
	changelog "github.com/zdirnecamlcs96/chronicle/core"
	"github.com/zdirnecamlcs96/chronicle/kit/internal/docmodel"
	chronicleschema "github.com/zdirnecamlcs96/chronicle/kit/schema"
)

// ExplainChanges is the single-commit form of Explain: it decorates changes as
// applied to before (which is not mutated), under the caller's schema. Rows
// align 1:1 with changes. It is what the kit's write path uses to compute a
// commit's readable sidecar at seal time — when external referents still exist
// and the caller's WithNames dictionary is fresh.
func ExplainChanges(before any, changes []changelog.Change, opts ...chronicleschema.Option) ([]Explained, error) {
	cfg := chronicleschema.New(opts...)
	cur, err := docmodel.Normalize(before) // deep copy: decoration replays in place
	if err != nil {
		return nil, err
	}
	rows, _, err := explainStep(&cfg, cur, changes)
	return rows, err
}

// Readable is the per-commit readable sidecar payload, stored OUTSIDE the hash
// seal (changelog.Annotator). Rows align 1:1 by ordinal with the commit's
// Changes. Error records a compute/persist failure at seal time, so readers
// can see and acknowledge the gap instead of mistaking it for pre-feature
// history. A payload never carries both.
type Readable struct {
	Rows  []ReadableRow `json:"rows,omitempty"`
	Error string        `json:"error,omitempty"`
}

// ReadableRow is Explained-lite: the self-describing subset a reader renders
// without schema options or chain replay, with display names frozen at seal
// time. At/Actor and container value trees stay with the sealed record and
// live decoration respectively.
type ReadableRow struct {
	Path    string   `json:"path"`
	Kind    string   `json:"kind"`
	From    string   `json:"from,omitempty"`
	To      string   `json:"to,omitempty"`
	Field   []string `json:"field,omitempty"`
	Element *Element `json:"element,omitempty"`
	Display *Display `json:"display,omitempty"`
}

// ReadableOf projects decorated rows to the stored lite form.
func ReadableOf(rows []Explained) Readable {
	out := Readable{Rows: make([]ReadableRow, len(rows))}
	for i, r := range rows {
		out.Rows[i] = ReadableRow{
			Path:    r.Path,
			Kind:    r.Kind,
			From:    r.From,
			To:      r.To,
			Field:   r.Field,
			Element: r.Element,
			Display: r.Display,
		}
	}
	return out
}
