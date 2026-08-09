// Package chroniclekit is the batteries-included layer over chronicle/core: it
// produces Changes from before/after states, converts to/from RFC 6902 JSON
// Patch, seals via the Service facade, and reconstructs/renders document state
// on read. It imports core; core never imports it. It is adapter-agnostic —
// construct it over any changelog.Service.
//
// The layer is split across five packages. This one is the facade: Kit wraps a
// Service, RecordUpdate/RecordChanges write, and State/StateAt/CommitSnapshot
// read. The pieces it delegates to are usable on their own:
//
//   - kit/schema (chronicleschema) — the Option vocabulary a caller uses to
//     declare its document's shape, plus the re-exported create/put/delete
//     kinds. The dotted path grammar and Apply live behind
//     kit/internal/docmodel, shared by the packages below.
//   - kit/diff (chroniclediff) — Diff turns before/after into Changes.
//   - kit/view (chronicleview) — Reconstruct replays Changes into state; Reader
//     adds snapshot- and cursor-accelerated reads.
//   - kit/explain (chronicleexplain) — Explain replays a chain into
//     display-ready rows, decorated at read time. Nothing display-shaped is ever
//     stored.
//   - kit/httpapi — an optional stdlib http.Handler over a Service.
//
// The KindCreate/KindPut/KindDelete constants are re-exported here for
// Operation users; chronicleschema holds the canonical definitions.
package chroniclekit
