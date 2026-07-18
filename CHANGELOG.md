# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Modules are versioned in lockstep. A single version covers every module and is
published as per-module Go tags: `core/vX.Y.Z`, `adapters/memory/vX.Y.Z`,
`adapters/sql/vX.Y.Z`, `adapters/clickhouse/vX.Y.Z`, and `kit/vX.Y.Z`.

## [Unreleased]

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

### Changed
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

[0.1.2]: https://github.com/zdirnecamlcs96/chronicle/releases/tag/core%2Fv0.1.2
[0.1.1]: https://github.com/zdirnecamlcs96/chronicle/releases/tag/core%2Fv0.1.1
[0.1.0]: https://github.com/zdirnecamlcs96/chronicle/releases/tag/core%2Fv0.1.0
