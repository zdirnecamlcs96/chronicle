# Design: optional per-change parent snapshot

**Date:** 2026-06-21
**Status:** approved — ready for implementation plan
**Scope:** `core` module only (package `changelog`)

## Problem

Today a `Change` records a single leaf edit: `Path` (a dotted JSON path such as
`items.k1.qty`) plus `From`/`To`. That is a precise diff of one field, but it
carries no surrounding context. To render a git-history-style view — "show me the
edit *in the shape of the object it lives in*" — a reader currently has only the
isolated leaf, or must replay the whole commit chain from root to reconstruct the
document. Snapshotting the whole document at every commit is rejected (storage
bloat; chronicle is a changelog, not a document store).

## Goal

Let a producer optionally attach, to each `Change`, a snapshot of the **enclosing
parent object** of the changed path — e.g. for `items.k1.qty` the object
`items.k1: {sku, qty, price}`. This gives git-`show`-like local context (the
siblings around the edited field) without snapshotting the entire document.

Example. Document:

```json
{ "items": { "k1": {"sku":"ABC","qty":3,"price":10}, "k2": {…} }, "status":"draft" }
```

Change `path="items.k1.qty"`, `From="3"`, `To="5"`, with snapshot of the
enclosing object:

```json
{ "sku":"ABC", "qty":3, "price":10 }
```

## Forced constraints (not choices)

1. **Source-agnostic.** The library never receives the document JSON — only
   `Change{Path, From, To}` strings. It therefore *cannot compute* the enclosing
   parent itself. The snapshot must be **producer-supplied** and stored as an
   opaque string, exactly like `From`/`To`. The library never parses it.

2. **Excluded from the commit hash.** `computeID` hashes
   `(parent, message, changes)`. If the snapshot entered the hash, the same
   logical edit with vs. without a snapshot would produce different commit IDs —
   breaking content-addressed dedup and changing every already-stored commit's
   ID. The snapshot is therefore **excluded from the hash**, like `Commit.At` and
   `Commit.Authors` already are. Trade-off accepted: the snapshot is
   non-authoritative context; `From`/`To` (the real diff) remain
   integrity-protected inside the hash.

3. **Storage-transparent.** All three adapters serialize `[]Change` as one JSON
   blob (`adapters/sql`: `JSON` column; `adapters/clickhouse`: `String`;
   `adapters/memory`: in-struct). A new `Change` field rides along with **no
   schema migration**.

## Decisions

| Decision | Choice | Why |
|---|---|---|
| Which "parent" | Structural — the enclosing JSON object of the changed path | Matches "outer parent, not whole document" |
| Placement | Per-`Change` field | Each change has its own path → its own enclosing parent; precise when one commit edits multiple subtrees |
| Shape | One opaque `string` | Mirrors `From`/`To`; producer decides content (recommended: the enclosing object's *before* state); YAGNI vs. explicit before/after |
| Producer | Caller supplies it | Forced by source-agnostic constraint |
| Hashing | Excluded | Forced by dedup + existing-ID stability |
| Storage | Existing JSON blob | No migration |

## Design

### 1. `core/change.go` — add one field

```go
type Change struct {
    At       time.Time `json:"at"`
    Actor    string    `json:"actor"`
    Path     string    `json:"path"`
    Kind     string    `json:"kind"`
    From     string    `json:"from,omitempty"`
    To       string    `json:"to,omitempty"`
    Snapshot string    `json:"snapshot,omitempty"` // NEW
}
```

`Snapshot` is appended last so the marshaled byte layout of the existing fields is
unchanged. Godoc documents it as: optional, producer-supplied, opaque context —
the enclosing parent object's state for `Path` (recommended: the *before* state);
**not** part of the commit hash; the library never interprets it.

### 2. `core/commit.go` — hash a projection that omits `Snapshot`

```go
// hashChange is the projection of Change folded into the commit hash. It omits
// Snapshot so the same logical edit hashes identically whether or not a snapshot
// rides along — preserving content-addressing and dedup, and keeping every
// pre-existing commit ID byte-for-byte unchanged. Its json tags MUST stay in
// lockstep with Change's hashed fields.
type hashChange struct {
    At    time.Time `json:"at"`
    Actor string    `json:"actor"`
    Path  string    `json:"path"`
    Kind  string    `json:"kind"`
    From  string    `json:"from,omitempty"`
    To    string    `json:"to,omitempty"`
}
```

`computeID` projects `[]Change` → `[]hashChange` before marshaling:

```go
hc := make([]hashChange, len(changes))
for i, c := range changes {
    hc[i] = hashChange{At: c.At, Actor: c.Actor, Path: c.Path, Kind: c.Kind, From: c.From, To: c.To}
}
payload, err := json.Marshal(hc)
```

Field order/tags/`omitempty` match the current `Change` marshaling exactly, so
the hashed bytes for any change are identical to today's — existing IDs do not
move.

### 3. Storage — no change

`json.Marshal(c.Changes)` in every adapter now emits `snapshot`; the read path
unmarshals it back into the new field. No adapter code or schema touched.

### 4. Recorder / Service / Log — no change

`Append(Change)` and `Seal([]Change, …)` already take whole changes; the producer
sets `Snapshot` before calling them. All porcelain and ports are transparent.

## Out of scope (YAGNI)

- The library computing the parent (it has no document).
- Reconstructing full document state / point-in-time reads.
- Explicit before+after snapshot fields.
- A per-`Commit` snapshot variant.
- Temporal (whole-document-at-parent-commit) snapshots — explicitly rejected.

## Tests / success criteria

1. `core/commit_test.go`: a commit's ID is **identical** whether or not
   `Snapshot` is set on its changes (proves hash exclusion and existing-ID
   stability).
2. Round-trip: `Append` a `Change` carrying `Snapshot` → `Commit` → read back via
   the memory Log → `Snapshot` preserved.
3. Conformance: add a `Snapshot` round-trip assertion to `RunLogConformance` so
   every adapter — including future ones with typed columns — must persist it.
4. `go build ./...` and `go test ./core/...` green.

## Files touched

- `core/change.go` — add field + godoc.
- `core/commit.go` — add `hashChange`, project in `computeID`.
- `core/commit_test.go` — hash-exclusion test.
- `core/recorder_test.go` (or `change_test.go`) — round-trip test.
- `core/conformance/conformance.go` — snapshot round-trip assertion.
- `README.md` — short "Optional: parent snapshots for richer diffs" subsection.
- `core/MODEL.md` — one mapping row (`git show` with context).

## Expected outcome

**API.** One new optional field, `Change.Snapshot`. Existing code compiles and
behaves identically — `Append`, `Commit`, `Seal`, every `Log` method, and the
conformance suite keep their signatures. A producer that wants richer diffs sets
`Snapshot` per change; a producer that does not is unaffected.

```go
rec.Append(changelog.Change{
    Actor: "alice", Path: "items.k1.qty", Kind: "put", From: "3", To: "5",
    Snapshot: `{"sku":"ABC","qty":3,"price":10}`, // enclosing object, before-state
})
commit, _ := rec.Commit(ctx)
// commit.ID is the SAME as it would be without Snapshot — dedup intact.
// commit.Changes[0].Snapshot survives a round-trip through any adapter.
```

**Integrity.** Commit IDs are unchanged for all existing data; `From`/`To` stay
hashed; the snapshot is stored, returned verbatim, and never hashed or
interpreted.

**Storage.** No migration on any backend. Existing rows read back with
`Snapshot == ""`; new rows carry whatever the producer supplied.

**Reader benefit.** A diff viewer can render the edited field inside its enclosing
object (siblings visible) — git-`show`-like context — at a per-change storage cost
of one object, independent of document size, with no full-document snapshot and no
chain replay.

**Non-goals confirmed unmet by design:** no full-document reconstruction, no
library-side parent computation, no new ports or capabilities.
