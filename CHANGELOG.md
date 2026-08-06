# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Modules are versioned in lockstep. A single version covers every module and is
published as per-module Go tags: `core/vX.Y.Z`, `adapters/memory/vX.Y.Z`,
`adapters/sql/vX.Y.Z`, `adapters/clickhouse/vX.Y.Z`, and `kit/vX.Y.Z`.

## [Unreleased]

Next tag is 0.3.0 — the identity change below is breaking.

### Changed
- **`kit`**: **BREAKING** — the generic `"id"` element-identity convention is
  now caller-declared via `WithIdentityFields(fields...)`. The kit ships no
  default: an array with neither a `WithArrayKeys` entry nor a usable declared
  generic field pairs positionally. This alters what `Diff` RECORDS, so declare
  it before the first write and keep it stable — a later declaration cannot
  re-key commits already sealed. `WithIdentityFields("id")` restores the
  previous behaviour exactly.

### Fixed
- **`kit`**: an array whose identity is a dot-path (`{"lines":
  "product.id"}`) now takes its element display name from the object that path
  descends into, falling back to the element root's name fields, the id→name
  index, then the identity value. Previously only the element root was tried,
  so an element embedding its entity rendered as a raw id. The same rule names
  the root of a whole-element `ValueNode` tree, which previously came back
  unnamed. A single-segment key path is unaffected. The id→name index itself is
  always keyed by the object the identity belongs to, so an element's own name
  is never filed against an id it merely carries.

### Added
- **`kit`**: `WithNames(map[string]string)` seeds the id→name index with pairs
  the replayed document cannot supply — ids referencing entities stored outside
  it. Keys normalise from plain or canonical-JSON form; document-derived names
  win. Data, not a resolver, so a schema declared in one process survives
  serialisation to another.
- **`kit`**: `ValueNode.Display` carries the resolved name when a leaf's
  `Value` is a known id, `""` otherwise. `Value` stays the canonical record and
  is never overwritten, so a display can show the name and keep the id.

## [0.2.0] - 2026-08-05

### Added
- **`core`**: chain verification — `VerifyChain(commits)` / `Verify(ctx, log, docID)`
  recompute the hash chain and detect tampering (`ErrHashMismatch`), broken
  linkage (`ErrBrokenChain`), and forks (`ErrFork`).
- **`core`**: optional capabilities `TailReader` (`CommitsAfter` cursor reads,
  oldest-first, `ErrNoSuchCommit` on an unknown cursor) and `Snapshotter`
  (opaque one-per-document snapshot cache), plus conformance suites
  `RunTailReaderConformance` / `RunSnapshotterConformance`. The mandatory
  `RunLogConformance` now also hash-verifies the stored chain.
- **`adapters/memory`, `adapters/sql`, `adapters/clickhouse`**: implement
  `TailReader` + `Snapshotter` (`snapshots` table on the durable backends).
- **`adapters/sql`, `adapters/clickhouse`**: `PruneSeen(ctx, olderThan)` for
  operator-cronned retention of the idempotency `seen` table.
- **`kit`**: `State` uses snapshot + tail replay when the backend exposes
  `Snapshotter` + `TailReader` — reads on long histories become O(commits since
  last read) instead of O(all commits). The snapshot is a lazily refreshed,
  best-effort cache.
- **`kit`**: backslash escaping in the dotted path grammar (`\.` = literal dot,
  `\\` = literal backslash) — object keys containing "." now survive
  Diff → seal → State and JSON-Pointer round-trips.
- **`core`**: incremental verification — `VerifyChainAfter(anchorID, tail)` /
  `VerifyAfter(ctx, log, docID, anchorID)` verify only the commits after a
  previously verified anchor via `TailReader`, O(commits since anchor) instead
  of O(all commits). `VerifyAfter` returns the new verified head to persist as
  the next anchor; tampering at or before a trusted anchor is by contract
  invisible (run a full `Verify` when the anchor's provenance is in doubt).
- **`kit`**: `StateAt` and `CommitSnapshot` use the snapshot + tail fast path
  when the target commit is at or after the stored snapshot — recent-history
  time-travel reads become O(commits since snapshot). Older targets fall back
  to full replay; historical reads never move the snapshot cache.
- **`core`**: `VerifyChain` / `VerifyChainAfter` now also recompute each
  commit's `Authors` from its `Changes` and flag a mismatch
  (`ErrAuthorsMismatch`). `Authors` is derived metadata outside the hash, so
  recomputation is what makes editing it after sealing detectable. `Commit.At`
  stays unauthenticated convenience metadata — the authenticated timeline is
  the hashed per-`Change` `At`.
- **`kit`**: `Diff` accepts schema-declaring options (`DiffOption`):
  `WithArrayKeys` (per-array element identity as an index-free schema path →
  dot-path into elements, `""` for a root array), `WithValueTypes`
  (caller-declared value-object shapes compared and recorded via their
  canonical scalar). `WithIgnoredFields` names bookkeeping fields — a bare
  name matches at any depth, an index-free schema path matches its field and
  subtree — but does NOT affect recording: the changelog stays a full data
  record; `Explain` flags their changes as `Bookkeeping` for displays to fold
  away. Arrays of objects pair by the configured key, else by a unique scalar
  `id` on every element, else positionally; arrays of scalars always pair by
  index. Keyed reordering alone records nothing, so
  replay reproduces the element set (survivors in before order, additions
  appended), not the after ordering. `RecordUpdate` forwards these via the
  new `WithDiffOptions` record option.
- **`kit`**: `Explain(commits, opts...)` — read-time display decoration. It
  replays the chain (same contract as `Reconstruct`) and returns per-change
  rows `{Change, Field, Element, Display, Bookkeeping, FromValue, ToValue}`:
  the field's human label trail
  (caller resolver via `WithLabels`, Title Case fallback), the keyed array
  element the change sits inside (`Trail`/`Name`/`ID`; whole-element
  add/remove = `Element` with empty `Field`), and display names for id-valued
  `From`/`To` resolved from id→name pairs in the surrounding revisions
  (`WithNameFields` picks the name field). Container `From`/`To` values also
  decompose into a `ValueNode` tree (labels, element identity, canonical
  scalars, bookkeeping marks, order — structure only, never formatting), so
  displays can break a whole-container change down per field without
  re-implementing the schema walk. Nothing new is stored — records written
  before this feature decorate
  identically, and the stored record, hash preimage, and storage layers are
  untouched.

### Changed
- **`kit`**: `Diff` now pairs arrays of objects by a unique scalar `id` field
  when every element on both sides has one (the documented generic identity
  convention) — reordering such an array no longer records per-index churn.
  Declare `WithArrayKeys` to choose a different key; arrays without usable
  identity keep the old positional behavior byte-for-byte.
- **`core`**: `Service.Seal` retries on `ErrParentConflict` now back off with
  full jitter (~75 ms worst case across 5 attempts) instead of hammering.
- **`kit`**: `Reconstruct` errors on a change `Kind` outside the kit vocabulary
  (create/put/delete) instead of silently treating it as a put. Histories
  containing out-of-vocabulary kinds now fail loudly on read.

## [0.1.2] - 2026-06-21

### Added
- **`kit` module** (`github.com/zdirnecamlcs96/chronicle/kit`) — one-stop kit over `core`:
  - `Diff(before, after)` — structural diff of two documents into `[]changelog.Change`,
    with round-trip reconstruction.
  - JSON-Patch (RFC 6902) interop — `Operation` type plus `FromChanges` / `ToChanges`.
  - `Kit` facade — `RecordUpdate` (seals a diff into a commit), idempotency via
    `WithIdempotencyKey`, `WithMessage`, and `State` / `StateAt` reconstruction.
  - Per-commit LCA snapshot derivation and `Reconstruct` / LCA-path helpers.
  - `kit/httpapi` — `Handler(svc)` exposing the changelog `Service` as a REST
    transport (commits, changes; snake_case JSON).
- Docs: `CONTRIBUTING.md` (design patterns and invariants) and specs for the
  chroniclekit design and per-commit LCA snapshots.

### Notes
- No functional changes to `core` or the adapters; they are re-tagged in lockstep.

## [0.1.1] - 2026-06-18

### Changed
- Hardened changelog integrity: hash framing, per-document idempotency, and
  length checks.
- Simplified `core` commit/service internals (`commit.go`, `service.go`,
  `capability.go`, `recorder.go`).

### Added
- Conformance coverage for the new integrity guarantees (`core/conformance`).

## [0.1.0] - 2026-06-16

### Added
- Initial release: `chronicle`, a durable, database-agnostic changelog library
  for Go — `core` plus `memory`, `sql`, and `clickhouse` adapters.

[0.2.0]: https://github.com/zdirnecamlcs96/chronicle/releases/tag/core%2Fv0.2.0
[0.1.2]: https://github.com/zdirnecamlcs96/chronicle/releases/tag/core%2Fv0.1.2
[0.1.1]: https://github.com/zdirnecamlcs96/chronicle/releases/tag/core%2Fv0.1.1
[0.1.0]: https://github.com/zdirnecamlcs96/chronicle/releases/tag/core%2Fv0.1.0
