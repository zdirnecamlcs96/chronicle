package changelog

import "time"

// Change is one recorded edit — a single line in a Commit's diff. The model is
// deliberately git-like: a Recorder stages Changes and seals a batch of them into
// one hash-chained Commit.
//
//   - At      when the edit happened; the Recorder stamps it from its own clock on
//             Append, overwriting any value you set.
//   - Actor   who made the edit (its author).
//   - Path    what was edited — a dotted path into the document (e.g. "items.k1.qty").
//   - Kind    a free-form operation label ("put", "delete", "create", …); the
//             package fixes no vocabulary.
//   - From/To the before/after values — usually JSON-encoded, but any string the
//             producer chooses. "" means "no value" (From on a create, To on a delete).
type Change struct {
	At    time.Time `json:"at"`
	Actor string    `json:"actor"`
	Path  string    `json:"path"`
	Kind  string    `json:"kind"`
	From  string    `json:"from,omitempty"`
	To    string    `json:"to,omitempty"`
}
