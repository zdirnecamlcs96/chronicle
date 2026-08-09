# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Modules are versioned in lockstep. A single version covers every module and is
published as per-module Go tags: `core/vX.Y.Z`, `adapters/memory/vX.Y.Z`,
`adapters/sql/vX.Y.Z`, `adapters/clickhouse/vX.Y.Z`, and `kit/vX.Y.Z`.

## [Unreleased]

Next tag is 0.3.0 — the identity change and the kit split below are both
breaking.

### Changed
- **`core`, `adapters/*`, `kit`**: **BREAKING** — the log is now
  **fork-tolerant**. A commit's parent is the snapshot the writer built
  against, stored verbatim: `RecordPatch` anchors to the head its state read
  reached (new `StateWithHead`), and new `WithParent` options
  (`changelog.WithParent` CommitOption, `changelog.WithSealParent` SealOption,
  `chroniclekit.WithParent` RecordOption) let any caller assert a base. Two
  writers racing the same document record two commits sharing a parent — a
  fork, folded last-write-wins in arrival order at read (replay behavior
  unchanged). Previously a concurrent writer was silently rebased over: the
  patch commit re-chained onto the newcomer's head while keeping `From` values
  diffed against the old state, permanently sealing before-values that were
  never true. Concurrency control is now explicitly out of scope — OCC/ACID
  belongs to the persistence layer or the producer.

  Removed: `changelog.ErrParentConflict`, `Seal`'s conflict retry/backoff,
  `changelog.ErrBrokenChain`, `changelog.ErrFork`, `VerifyChainAfter`,
  `conformance.RunSerializableAppend`. Added: `changelog.ErrMissingParent`,
  `changelog.VerifyCommits`, a `ForkAppend` subtest in `RunLogConformance`.
  `VerifyChain` now validates ancestry (hashes, authors, every parent exists)
  instead of linearity; `VerifyAfter` is a content-only incremental check.

  SQL adapter migration — the anti-fork `UNIQUE(doc_id, parent)` becomes a
  plain index. `Migrate()` performs the swap automatically; manual SQL:

  ```sql
  ALTER TABLE commits DROP INDEX uq_doc_parent, ADD INDEX idx_doc_parent (doc_id, parent);
  ```

  Existing linear histories verify unchanged (a line is a degenerate tree);
  stored data and hashes are untouched.

- **`kit`**: **BREAKING** — `chroniclekit.New` now takes a `changelog.Log`
  instead of a `changelog.Service`, and builds the `Service` itself. A caller
  picks a backend and nothing else; `Service` becomes an advanced detail rather
  than a required step. `NewWithService(svc)` is the old shape, for callers that
  already hold a `Service` or wrap one — `kit/httpapi` is one.

  ```go
  // before                                  // after
  svc := changelog.NewService(log)           k := chroniclekit.New(log)
  k := chroniclekit.New(svc)
  ```

- **`kit/schema`**: **BREAKING for anyone importing the helpers** — the eleven
  path and JSON functions (`SplitPath`, `JoinPath`, `ChildPath`, `ParentPath`,
  `AsIndex`, `IsContainer`, `CanonJSON`, `ParseJSON`, `Normalize`,
  `ElemKeyValue`, `Apply`) moved to `kit/internal/docmodel` and are no longer
  importable. They existed so `chroniclediff`, `chronicleview`, and
  `chronicleexplain` could share them, not as vocabulary a caller declares.
  `chronicleschema` drops from 22 exported identifiers to 14, all of them
  options you set. `KindCreate`/`KindPut`/`KindDelete` moved with `Apply` but
  are re-exported, so `chronicleschema.KindPut` still resolves unchanged.

- **`kit`**: **BREAKING** — the kit is now five packages instead of one. The
  facade keeps its import path and every method: `chroniclekit.Kit`,
  `Service`, `RecordUpdate`, `RecordChanges`, `State`, `StateAt`,
  `CommitSnapshot`, `RecordOption`/`WithMessage`/`WithIdempotencyKey`/
  `WithDiffOptions`, `Operation`/`FromChanges`/`ToChanges`, and
  `KindCreate`/`KindPut`/`KindDelete` are all unchanged. `kit/httpapi` keeps
  every route it had (see Added for the one it gained). What moved:

  | before | after |
  |---|---|
  | `chroniclekit.Diff` | `chroniclediff.Diff` (`kit/diff`) |
  | `chroniclekit.DiffOption` | `chronicleschema.Option` (`kit/schema`) — **renamed** |
  | `chroniclekit.ValueType`, `.WithArrayKeys`, `.WithIdentityFields`, `.WithLabels`, `.WithIgnoredFields`, `.WithValueTypes`, `.WithNameFields`, `.WithNames` | `chronicleschema.*` (`kit/schema`) |
  | `chroniclekit.Explain`, `.Explained`, `.Element`, `.Display`, `.ValueNode` | `chronicleexplain.*` (`kit/explain`) |
  | `chroniclekit.Reconstruct` | `chronicleview.Reconstruct` (`kit/view`) |

  `DiffOption` is renamed because the name was always a misnomer:
  `WithLabels`, `WithNames`, `WithNameFields`, and `WithIgnoredFields` never
  reach `Diff` at all. The option vocabulary is shared by both sides, so it
  now lives in the package neither side owns.

  Most call sites migrate with two substitutions — but note that
  `WithMessage`, `WithIdempotencyKey`, and `WithDiffOptions` are `RecordOption`s
  and stay on `chroniclekit`:

  ```
  s/chroniclekit\.DiffOption/chronicleschema.Option/g
  s/chroniclekit\.With\(ArrayKeys\|IdentityFields\|Labels\|IgnoredFields\|ValueTypes\|NameFields\|Names\)/chronicleschema.With\1/g
  ```

- **`kit`**: **BREAKING** — the generic `"id"` element-identity convention is
  now caller-declared via `WithIdentityFields(fields...)`. The kit ships no
  default: an array with neither a `WithArrayKeys` entry nor a usable declared
  generic field pairs positionally. This alters what `Diff` RECORDS, so declare
  it before the first write and keep it stable — a later declaration cannot
  re-key commits already sealed. `WithIdentityFields("id")` restores the
  previous behaviour exactly.

### Fixed
- **`kit`**: the "commit not found" errors raised by the read side now carry a
  `chronicleview:` prefix rather than `chroniclekit:`. Not previously
  documented as stable, but a consumer matching on the string will notice.
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
- **`kit/schema`**: `WithStrictIdentity()` makes an array of objects with no
  usable element identity a write-time `chroniclediff.ErrNoIdentity` instead of
  a silent fall back to positional pairing. Identity shapes what is *recorded*,
  so forgetting `WithIdentityFields` produced a successful write, a plausible
  history, and green tests — with no way to re-key the commits afterwards. This
  makes the omission fail loudly at the boundary where it is still fixable.
  Opt-in: the default stays positional, which is the right answer for arrays
  where order is the data. Scalar, empty, and keyed arrays pass regardless.
- **`kit`**: `Kit.RecordPatch(ctx, docID, ops, opts...)` seals an RFC 6902
  patch by applying it to the document's state and diffing the result. Sealing
  `ToChanges(ops)` directly records changes with no `From` — legal, and it
  replays correctly, but `chronicleexplain.Explain` then has no before-values
  and the diff view degrades with nothing on the wire to signal why. Applying
  first also pairs array elements by declared identity instead of trusting the
  client's indices. Unlike `ToChanges`, it rejects `move`/`copy`/`test` with
  `ErrUnsupportedOp` rather than skipping them: dropping an op silently seals a
  commit recording an edit the client did not send.
- **`kit/httpapi`**: `POST /commits` accepts `patch` (an RFC 6902 operation
  list, sealed through `RecordPatch`) as a third write shape beside `changes`
  and `before`/`after`, and a `schema` object carrying the write half of the
  vocabulary — `array_keys`, `identity_fields`, `strict_identity`. The read
  route has shipped its options over the wire since 0.2.0; the write route had
  no equivalent, so a producer writing over HTTP could not declare the one
  thing that must be declared *before* the first write. `value_types` still has
  no wire form — `ValueType.Canon` is a Go func. `ErrUnsupportedOp` and
  `chroniclediff.ErrNoIdentity` map to `400`; both are caused by what the
  client sent, and neither reaches the log. The full request lifecycle is
  walked through in [sealing RFC 6902 patches](docs/patches.md).
- **`kit`**: `chronicleview.Reader` — the snapshot- and cursor-accelerated read
  side on its own, without the write facade. `chroniclekit.Kit` now delegates
  its three read methods to one.
- **`kit/schema`**: `Config` and its accessors are public, so the schema a
  caller declares can be inspected by the packages that consume it. (An earlier
  draft of this release also exported the path and JSON helpers; they went back
  behind `kit/internal/docmodel` before the tag — see Changed. Nothing was ever
  published with them exported.)
- **`kit`**: `WithActor(actor)` attributes every change in a record call to one
  actor, which is what makes `Commit.Authors` non-empty. Previously the only way
  to stamp an actor was to call `chroniclediff.Diff`, loop over the result, and
  seal with `RecordChanges` — a lower layer and an extra import for the one
  field the library exists to capture. It fills a **blank** `Change.Actor` only,
  so an actor set deliberately per change still wins and mixed-attribution
  commits stay expressible.
- **`kit`**: `WithNames(map[string]string)` seeds the id→name index with pairs
  the replayed document cannot supply — ids referencing entities stored outside
  it. Keys normalise from plain or canonical-JSON form; document-derived names
  win. Data, not a resolver, so a schema declared in one process survives
  serialisation to another.
- **`kit`**: `ValueNode.Display` carries the resolved name when a leaf's
  `Value` is a known id, `""` otherwise. `Value` stays the canonical record and
  is never overwritten, so a display can show the name and keep the id.
- **`kit/httpapi`**: `POST /explain` — `{doc, limit?, options?}` returns the
  document's commits newest-first with every change display-decorated, so a
  browser client needs no schema logic. `options` carries the read-side
  vocabulary that survives JSON (`array_keys`, `identity_fields`,
  `name_fields`, `ignored_fields`, `names`); each is skipped when empty, since
  the replacing options would otherwise wipe their defaults. `limit` trims the
  response, never the replay — decoration is derived from the chain's root, so
  the handler always replays the whole history and slices afterwards.
  `WithLabels` has no wire form (it is a func); labels fall back to Title Case
  and a client wanting its own i18n translates from each row's `path`.
- **`kit/explain`**: **BREAKING for JSON consumers** — `Explained`, `Element`,
  `Display`, and `ValueNode` now carry snake_case `json` tags with
  `omitempty`. Previously untagged, so marshalled output used Go field names
  (`"Bookkeeping"`, `"FromValue"`); it is now `"bookkeeping"`, `"from_value"`,
  and absent when empty. The Go API is unchanged.
- **`kit/httpapi`**: `POST /commits` accepts an `actor` field. A non-empty
  value feeds `WithActor`, filling the blank `Change.Actor` on every change
  the write produces (an explicit per-change actor still wins) — previously
  the only way to attribute a write over HTTP was to set `Actor` on each
  change by hand.
- **`kit/httpapi`**: `GET /state?doc=&at=` returns a document's reconstructed
  state — at HEAD, or as of the commit named by `at`. An unknown `at` is
  `404`.
- **`kit/httpapi`**: `GET /verify?doc=` runs `VerifyChain` over a document's
  full history and reports the result as `{ok, commits, head?}` on success or
  `{ok:false, commits, error}` on a broken chain. The response is always
  `200` — a failed verification is a result the caller asked for, not a
  transport error.
- **`kit/httpapi`**: `GET /commits?doc=&limit=&after=` gains cursor
  pagination. `after=<commit id>` (requires `doc`) returns the commits
  strictly after it, oldest-first — deliberately the reverse of the route's
  default newest-first order, so a caller can advance the cursor by the last
  id it saw. Prefers the backend's `TailReader` when one is exposed, and
  falls back to a full read otherwise; an unknown `after` is `404`.

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
