# Design: chroniclekit — the one-stop kit over chronicle

**Date:** 2026-06-21
**Status:** approved (design) — pending spec review
**Scope:** a NEW sibling module `kit/` (package `chroniclekit`). `core` is untouched except an optional reuse of the existing `Change.Snapshot` field.

## Problem

`chronicle/core` is deliberately a pure recorder: you hand it fully-formed
`Change`s and it seals them into a hash-chained, content-addressed history behind
the `Log` port. It does NOT produce changes (no diff/compare), does not parse
`Path`, ships no transport, and does not reconstruct document state. Assembling a
working audit feature on top therefore means writing the same four pieces by hand
every time: produce changes, seal them, expose them over HTTP, and render/replay
them.

`chroniclekit` is the batteries-included layer that provides those four pieces on
top of `core`, so an application gets a one-call audit experience while `core`
stays pure.

## Architecture & dependency rule

```
your app
  └─ chroniclekit            NEW module …/chronicle/kit (package chroniclekit)
       ├─ diff.go            Diff(before, after) → []changelog.Change
       ├─ jsonpatch.go       FromChanges / ToChanges (RFC 6902 interop)
       ├─ kit.go             Kit facade: RecordUpdate / RecordChanges
       ├─ view.go            Reconstruct (replay) · StateAt · parent context
       └─ httpapi/           Handler(svc) http.Handler
       uses → chronicle/core (+ a changelog.Service the caller injects)
```

- **One-way dependency:** `chroniclekit → core`. `core` never imports the kit
  (same rule as adapters). Enforced by module layout.
- **Adapter-agnostic:** the kit operates over an injected `changelog.Service`
  (or `changelog.Log`); it imports **no** adapter. The caller picks
  memory/sql/clickhouse. The kit needs **no database driver** → it is
  **stdlib + core only**, a single module with sub-packages.
- **The kit owns the vocabulary `core` refuses:** the `Path` grammar, value
  encoding, comparison, JSON Pointer mapping, and state reconstruction all live
  here, where a document and a grammar exist.

## Path grammar (kit-internal)

Dotted segments; array indices are ordinary numeric segments:

```
status                 → top-level key
items.0.qty            → items[0].qty
items.k1.price         → nested object
```

- Split/join on `.`. A segment is a map key or (when the target is an array) a
  numeric index — resolved against the actual value's type at apply time, exactly
  as JSON Pointer does.
- `jsonpatch.go` converts kit path ↔ RFC 6901 JSON Pointer (`items.0.qty` ↔
  `/items/0/qty`), applying `~0`/`~1` escaping. `core.Change.Path` stays an
  opaque string; the grammar is a kit concern only.

(Decision: dotted-all over bracket notation `items[0]` — trivial split/join, no
custom parser, and the public RFC6902 surface presents standard JSON Pointer
anyway.)

## Components

### 1. Change production — `diff.go`, `jsonpatch.go`

```go
func Diff(before, after any, opts ...DiffOption) ([]changelog.Change, error)
```

- **JSON-normalize:** `json.Marshal` then `json.Unmarshal` both sides into
  `any` (so structs honour their json tags and maps/structs diff uniformly),
  then deep-diff.
- **Walk:**
  - both objects → recurse per key; key only in `before` → `delete`; only in
    `after` → `create`; in both → recurse.
  - both arrays → positional diff by index (documented limitation: a mid-array
    insertion reads as a run of edits + a tail add; good enough for v1, can gain
    a keyed/LCS strategy later via a `DiffOption`).
  - leaf change (scalar differs, or types differ) → `put` with
    `From = canonicalJSON(before)`, `To = canonicalJSON(after)`.
- Emits `Change{Path, Kind, From, To}`. `Actor`/`At` are set by the caller /
  Recorder, not the differ.
- `Diff(x, x)` returns no changes.

```go
type Operation struct { Op, Path string; Value json.RawMessage `json:",omitempty"` }
func FromChanges(cs []changelog.Change) []Operation        // export; drops From (RFC6902 is forward-only) — lossless re: apply
func ToChanges(ops []Operation) ([]changelog.Change, error) // ingest; From="" (documented), recover by diffing against prior state
```

`Kind` ↔ `op` map: `put`↔`replace`, `create`↔`add`, `delete`↔`remove`. `move`/
`copy`/`test` are not emitted; on ingest, `move`/`copy` expand to remove+add,
`test` is dropped.

### 2. Seal facade — `kit.go`

```go
type Kit struct{ /* svc changelog.Service */ }
func New(svc changelog.Service) *Kit

func (k *Kit) RecordUpdate(ctx context.Context, docID string, before, after any,
        opts ...RecordOption) (changelog.Commit, error)   // Diff(before,after) → Service.Seal
func (k *Kit) RecordChanges(ctx context.Context, docID string, cs []changelog.Change,
        opts ...RecordOption) (changelog.Commit, error)    // pass-through seal
```

`RecordOption` carries actor, message, and idempotency key (forwarded to
`Service.Seal` / `WithIdempotencyKey`). `RecordUpdate` with no diff returns
`changelog.ErrEmptyChanges` (reuses core's sentinel — nothing changed).

### 3. HTTP transport — `httpapi/`

```go
func Handler(svc changelog.Service) http.Handler   // stdlib http.ServeMux, method-pattern routes
```

| Route | Maps to |
|---|---|
| `POST /commits` `{doc_id, changes[]?, before?, after?, message?, idempotency_key?}` | `Kit.RecordChanges` or `Kit.RecordUpdate` |
| `GET /commits?doc=&limit=` | `Service.Commits` (or `AllCommits` if `doc` omitted) |
| `GET /commits/{id}` | `Service.Get` |
| `GET /changes?doc=&limit=` | flattened change feed from commits |

JSON in/out; errors map to status codes (`ErrEmptyChanges`→400, not-found→404).
Auth/middleware/TLS remain the caller's. (Reference shape:
go-crdt-playground `changelog-server/httpapi/handler.go`.)

### 4. Read / render — `view.go`

```go
func Reconstruct(commits []changelog.Commit) (map[string]any, error)            // replay oldest→newest
func (k *Kit) StateAt(ctx context.Context, docID, commitID string) (map[string]any, error)
func (k *Kit) State(ctx context.Context, docID string) (map[string]any, error)  // at HEAD
```

- **Replay:** start empty; for each change in commit order apply `put`/`create`
  (set value at `Path` = `json.Unmarshal(To)`) or `delete` (remove at `Path`).
  `Service.Commits` returns newest-first → reverse before replay. `StateAt`
  replays up to and including `commitID`.
- **Parent context for richer diffs (the resolution of the snapshot/LCA
  thread):** because the kit holds the reconstructed document AND the grammar,
  it derives the enclosing-object context of any change on read — no producer
  effort. If a `Change` already carries `core.Change.Snapshot` (ground-truth
  captured at write time), the kit prefers it; otherwise it derives one from the
  replayed state. Write-time snapshot and read-time derivation are complementary,
  not redundant.

## Decisions

| Decision | Choice | Why |
|---|---|---|
| Module/package | `kit/` · `chroniclekit` | matches `core/`+`adapters/*` layout; rename trivially |
| Driver deps | none (stdlib + core) | operates over injected `Service`; caller picks adapter |
| Diff input | JSON-normalize | structs (json tags) and maps diff uniformly |
| Path grammar | dotted-all (`items.0.qty`) | no custom parser; RFC6902 surface stays standard |
| Array diff | positional (v1) | simple; keyed/LCS later via `DiffOption` |
| `core.Change.Snapshot` | **keep** | write-time = ground truth, read-time = derived; complementary |
| Transport | stdlib `http.ServeMux` | no framework dependency |

## Out of scope (v1)

- Smart (keyed/LCS) array diffing — option hook left, not built.
- Auth, middleware, TLS, client SDKs — caller's.
- Changing `core` (other than reusing the existing `Snapshot` field on read).
- A bundled default adapter — caller injects the `Service`.

## Tests / success criteria

1. `diff`: `Diff(x,x)` empty; `Diff(a,b)` then apply to `a` yields `b`
   (round-trip); object add/remove/nested/array/leaf cases; type-change case.
2. `jsonpatch`: RFC 6902 examples convert both ways; `FromChanges`→`ToChanges`
   round-trips path+op+value (From loss documented & asserted).
3. `kit`: `RecordUpdate` seals exactly the diffed changes; empty diff →
   `ErrEmptyChanges`; idempotency key replays one commit (against memory adapter).
4. `view`: `Reconstruct` rebuilds known state from a commit sequence; `StateAt`
   stops at the right commit; parent-context prefers `Snapshot` when present.
5. `httpapi`: each route via `httptest` against an in-memory `Service`; status
   codes for empty/not-found.
6. `go build ./... && go test ./...` green across the workspace; gofmt clean.

## Expected outcome

A new module `…/chronicle/kit` giving an application a one-call audit experience —
diff a before/after, seal it, serve it over HTTP, and replay/render it — while
`core` remains the pure, dependency-light recorder. The recurring "produce /
compare / transport / render" work moves out of every consumer and into one
reusable, adapter-agnostic, driver-free package. Every cross-cutting idea raised
during design (state diffing, JSON-Patch interop, parent/LCA snapshot context)
lands here, where a document, a grammar, and comparison exist — leaving `core`'s
boundary intact.
