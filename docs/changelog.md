---
title: Changelog
permalink: /documentation/changelog/
eyebrow: changelog
source: changelog.md
summary: >-
  Every release, newest first — what was added, changed, broken, and fixed,
  per module. Mirrors the repository's CHANGELOG.md.
---
<!-- Mirror of /CHANGELOG.md — update both when cutting a release. -->

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Modules are versioned in lockstep. A single version covers every module and is
published as per-module Go tags: `core/vX.Y.Z`, `adapters/memory/vX.Y.Z`,
`adapters/sql/vX.Y.Z`, `adapters/clickhouse/vX.Y.Z`, and `kit/vX.Y.Z`.

## [0.5.1] - 2026-08-27

Tags all modules in lockstep; only `adapters/clickhouse` changes. No core
requirement bump — core is unchanged, so the `core v0.5.0` requirement in
kit and the adapters stays valid.

### Changed
- **`adapters/clickhouse`**: **BREAKING** — `New` and `Open` now require a
  caller-declared table-name prefix and return an error when it is missing
  or not a plain identifier (`^[A-Za-z_][A-Za-z0-9_]*$`). The four tables
  are derived from it as `<prefix>_changelog_commits`,
  `<prefix>_changelog_seen`, `<prefix>_changelog_snapshots`, and
  `<prefix>_changelog_annotations`; a trailing underscore on the prefix is
  stripped, so `"myapp"` and `"myapp_"` are equivalent. New signatures:
  `New(db *sql.DB, prefix string) (*Log, error)` and
  `Open(ctx context.Context, dsn string, prefix string, opts ...Option)
  (*Log, error)`. An existing deployment keeps its history by renaming the
  old fixed-name tables before upgrading, e.g.
  `RENAME TABLE commits TO myapp_changelog_commits` (likewise `seen`,
  `snapshots`, `annotations`) for prefix `"myapp"`.

## [0.5.0] - 2026-08-17

Ships two-phase like 0.4.0: `core/v0.5.0` first, kit and adapter tags after
their core requirement bumps. The preimage change below is why the order
matters.

### Changed
- **`core`**: **BREAKING** — the commit-ID preimage now begins with the
  domain-separation tag `chronicle.commit.v1\n`, ahead of the length-framed
  `(parent, message, changes)` fields. Every commit ID changes; a history
  sealed by an earlier version fails verification under this one, and there
  is no migration — re-seal or stay on 0.4.x. The tag is format-evolution
  insurance bought while it is still cheap: any future encoding change bumps
  the version tag instead of silently orphaning history, and hashed object
  types are domain-separated from one another (checkpoints use
  `chronicle.checkpoint.v1\n`). New golden tests pin the exact encoding — if
  one ever fails, the encoding changed, and the fix is a version bump, not
  updating the test.
- Docs: the README now leads with tamper-evident audit logging (the
  git-style framing stays as the mechanism explanation, not the pitch) and
  gains a "What tamper-evident means here" table stating, per threat, what
  the chain detects on its own, what needs an external anchor, what needs
  `Reconcile`, and what needs commit signing (below) — the forged-but-
  validly-chained-append row flips from "not detected" to "detected" now
  that signing has shipped.

### Added
- **`core`**: Ed25519 commit signing. `Commit` gains `SigKeyID`/`Signature`,
  both outside the hash preimage — a signature authenticates the hash, so it
  cannot also be part of what it authenticates. `Recorder.WithSigner`/
  `Service.WithSigner` sign every commit sealed thereafter under a
  caller-chosen `keyID`, chaining onto the constructor like `WithClock`. The
  signed message is the domain tag `chronicle.sig.v1\n` followed by the
  commit `ID` — since `ID` already covers `(Parent, Message, Changes)` and,
  via `Parent`, its ancestry, signing `ID` alone authenticates the whole
  commit and its lineage. A signing failure fails the `Commit`/`Seal` call
  and restores staged changes, like any other error — authoritative, not
  best-effort, unlike the readable sidecar. `VerifySignatures` classifies a
  document's full history into a `SignatureReport`
  (`Unsigned`/`Valid`/`Invalid`/`UnknownKey`, report-don't-verdict like
  `VerifyCheckpoint`) against a caller-supplied `KeyResolver`. New golden
  tests pin the signed-message encoding, and `RunLogConformance` gains
  `SignatureFieldsRoundTrip`.
- **`kit`**: `WithSigner(signer, keyID)` constructor option forwards to the
  underlying `Service`'s `WithSigner`, so every seal path the `Kit` owns
  (`RecordUpdate`, `RecordPatch`, the baseline seal) comes back signed.
  Last-option-wins over a signer already configured on a `Service` passed to
  `NewWithService`. Unlike `WithReadable`, a `Service` without the capability
  is **not** a silent no-op: every subsequent seal call fails loudly with the
  new `ErrSignerUnsupported` — an unsigned commit under a configured signer
  would be forgery-shaped.
- **`adapters/sql`, `adapters/clickhouse`**: `commits` gains `sig_key_id`/
  `signature` columns (nullable in SQL, empty-string default in ClickHouse —
  both round-trip an unsigned commit's zero value); `Migrate` adds them to an
  existing table on next boot.
- **`core`**: `Tipper` optional capability — `Tips(ctx, docID)` returns a
  document's tips (commits no other commit lists as parent) in append order
  without fetching the chain: one on a linear chain (equal to `Head`), one
  per branch after a fork. `RunTipperConformance` joins the opt-in suites;
  all three adapters implement it (SQL as an anti-join on the existing
  `(doc_id, parent)` index).
- **`core`**: inventory checkpoints and anchoring. `ComputeCheckpoint`
  digests the log's inventory — per document its tips and commit count —
  under the `chronicle.checkpoint.v1\n` tag; the digest frames each entry's
  head count (self-delimiting encoding, so a `Commits` value can never be
  mistaken for a following head's own length prefix); entries and tips are
  sorted before hashing, so the digest is order-independent, and
  `Checkpoint.At` stays outside it like `Commit.At`. `VerifyCheckpoint` compares a recorded
  checkpoint against the live log with growth-aware semantics — chains
  legitimately extend, so a recorded tip is checked for existence rather than
  tip-ness — and reports `MissingDocs`, `MissingHeads`, `ShrunkDocs`, and
  `Doctored` (the checkpoint's own digest fails, caught before touching the
  log). `Anchorer` is a consumer port, deliberately not an adapter
  capability: it must write somewhere the database owner cannot reach, since
  an anchor stored in the database it guards is decoration. Together these
  close the two gaps a full `Verify` cannot see: whole-document deletion and
  tail truncation.
- **`kit`**: `Kit.Reconcile(ctx, docID, actual, opts...)` — does the log
  agree with the live row? Replays the document from the log and diffs it
  against the caller-projected `actual` (same projection contract as
  `RecordUpdate`), returning field-level `Drift` (`From` = what the log
  says, `To` = what the database holds) with deliberately no verdict: the
  same signal means a write bypassed chronicle or a sealed commit's database
  write failed, and the operator judges. `WithIgnoredFields` entries are
  genuinely excluded from the comparison here (elsewhere they remain
  display-only). `Kit.ReconcileSweep(dbDocIDs)` covers the half per-document
  reconcile cannot: documents present in only the log or only the database
  (`ErrNoIndexer` when the backend lacks `Indexer`). The chain proves the
  log was not edited; reconcile is what argues it is complete.
- **`kit/httpapi`**: `GET /verify` responses gain `heads` — every tip of the
  document's history, in chronological order (`head` is unchanged: the
  backend's latest-arrival tip). More than one entry means the history holds
  a fork; the old singular-only field invited reading `/verify` as a
  single-tip walk, which it never was.

### Fixed
- **`kit`**: two seal-but-cannot-replay gaps, both found by the new
  `FuzzDiffApplyRoundTrip` harness (property: `Diff(before, after)` replayed
  onto `before` reproduces `after`, over every JSON root shape):
  - an empty-string object key produced a path colliding with the root-path
    encoding, sealing a change `Apply` rejected forever. By policy `Diff`
    now skips empty-string keys at any depth and logs a warning — they are
    never recorded.
  - changing the root's container type (array ↔ object) sealed a whole-root
    replace with an empty path that `Apply` rejected. An empty path with
    kind `put` now means "replace the root value"; `create`/`delete` there
    still error. The read side was widened to match: `Reconstruct`/`State`
    now return the replayed root as-is (`any`), so non-object roots replay
    end-to-end.
- **`adapters/sql`**: concurrent appends to one document could exhaust the
  deadlock retry budget. The head read (`… ORDER BY seq DESC LIMIT 1 FOR
  UPDATE`) gap-locks the tail of the document's index range, which deadlocks
  against concurrent tail inserts — a convoy that recurs on every growing
  document. Appenders now first claim the document's row in the new
  `doc_locks` table (a plain record lock, never a gap lock), so
  same-document writers queue instead of colliding; `Migrate` creates the
  table on next boot. The head read no longer takes `FOR UPDATE` at all —
  the `doc_locks` claim provides the per-document exclusion — removing the
  residual cross-document gap-lock class.

## [0.4.0] - 2026-08-12

Released two-phase: `core/v0.4.0` shipped first, and the kit and adapter tags
followed once their `go.mod` core requirement was bumped to it.

### Added
- **`core`**: `Annotator` optional capability — at most one opaque annotation
  per `(docID, commitID)`, latest write wins, stored **outside** the hash
  seal. Non-authoritative by construction: deleting annotations is always
  safe, readers fall back to live decoration. `RunAnnotatorConformance` joins
  the opt-in conformance suites.
- **`adapters/sql`, `adapters/clickhouse`, `adapters/memory`**: implement
  `Annotator` (new `annotations` table; `Migrate` creates it on next boot —
  `CREATE TABLE IF NOT EXISTS`, safe on every startup).
- **`kit`**: `WithReadable()` constructor option — the readable sidecar.
  Each `RecordUpdate`/`RecordPatch` seal (baselines included) also decorates
  the sealed changes under the call's diff options (`ExplainChanges`, the new
  single-commit form of `Explain`) and stores the rows per commit via the
  backend's `Annotator`. Fresh `WithNames` pairs passed in `WithDiffOptions`
  freeze into the sidecar, so a referent's name survives its later deletion or
  rename — the one thing read-time decoration cannot recover. Best-effort by
  contract: a sidecar failure never fails the commit; a persistent one lands
  an `{"error": …}` stub so readers can acknowledge the gap. Direct
  `RecordChanges` seals store nothing (no before-state; absence means
  "decorate live", like all pre-feature history).
- **`kit`**: `Kit.Explain` — fetch + replay + decorate in one call, overlaying
  name-resolution fields (`Display`, `Element.Name`) from stored sidecars
  (stored wins; labels and trails always render live). `Kit.Readables` returns
  the raw stored payloads.
- **`kit/httpapi`**: `Handler` takes `chroniclekit.Option`s
  (`Handler(svc, chroniclekit.WithReadable())` opts a server's writes in);
  `POST /commits` `schema` gains `names`; `GET /changes` rows carry
  `readable` / `readable_error` when a sidecar exists; `POST /explain` now
  overlays stored names via `Kit.Explain`.

### Fixed
- **`kit`**: the readable sidecar's failure stub no longer stores the raw
  backend error. A `SaveAnnotation` failure logged the driver's error text
  into the sidecar, where `GET /changes` served it verbatim as
  `readable_error`; it now logs server-side and stores a generic `"sidecar
  unavailable"` stub.

### Changed
- Docs: "display metadata is never stored" is now scoped to the hash seal —
  the readable sidecar is an optional, non-authoritative projection outside it
  (docs/concepts.md, docs/design.md, docs/reference.md, docs/kit.md).

## [0.3.1] - 2026-08-11

Kit-only release: `kit/v0.3.1` is the sole tag. Core and the adapters are
unchanged and stay at 0.3.0; the kit continues to require `core v0.3.0`.

### Added
- **`kit`**: `WithCaptureBaseline(message, actor)` RecordOption — onboards a
  document that existed before recording began. When the delta is non-empty,
  `before` is non-nil, and the document has no commits yet, `RecordUpdate`
  first seals `Diff(nil, before)` as a baseline root commit (under the same
  diff options, so keyed arrays record the element identity the delta uses),
  then the caller's delta parented to it. The returned commit is always the
  delta. The two seals are not atomic; a baseline-only chain heals on the next
  write, and a deduped retry never seals a second baseline. Without the option
  behavior is unchanged: the chain roots at the first delta, and
  reconstruction only ever covers fields touched since capture began —
  docs/kit.md shows both the onboarding call and the late-capture healing
  pattern for chains already recorded without a baseline.
- **`kit`**: `Explain` honors `WithValueTypes` inside container values: Canon
  decides before recursion, so a nested value-object renders as its canonical
  scalar leaf instead of decomposing field-by-field, and a raw-stored root
  keeps scalar semantics. Read-time only — stored records are unchanged.

### Fixed
- **`kit`**: `Diff` validates that a `ValueType` Canon returns a JSON scalar
  and refuses with `chroniclediff.ErrBadCanon` otherwise — a non-JSON return
  was sealed verbatim into `From`/`To` and made the history unparseable by
  `Explain` and `State`. `Explain` treats an invalid return as declined and
  degrades to the per-field tree, since display must not error.

## [0.3.0] - 2026-08-09

The identity change and the kit split below are both breaking.

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

[0.4.0]: https://github.com/zdirnecamlcs96/chronicle/releases/tag/core%2Fv0.4.0
[0.3.1]: https://github.com/zdirnecamlcs96/chronicle/releases/tag/kit%2Fv0.3.1
[0.3.0]: https://github.com/zdirnecamlcs96/chronicle/releases/tag/core%2Fv0.3.0
[0.2.0]: https://github.com/zdirnecamlcs96/chronicle/releases/tag/core%2Fv0.2.0
[0.1.2]: https://github.com/zdirnecamlcs96/chronicle/releases/tag/core%2Fv0.1.2
[0.1.1]: https://github.com/zdirnecamlcs96/chronicle/releases/tag/core%2Fv0.1.1
[0.1.0]: https://github.com/zdirnecamlcs96/chronicle/releases/tag/core%2Fv0.1.0
