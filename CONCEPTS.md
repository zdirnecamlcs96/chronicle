# Concepts

The design patterns and algorithms chronicle is built on, and where each one
lives in the code. Reading order roughly follows the write → verify → read
lifecycle.

## Hash chain

Every commit's ID is `SHA-256(parent ID, message, changes)` — see `computeID`
in [`core/commit.go`](core/commit.go). Because the parent's ID is *inside* the
hash, altering any historical commit changes every ID after it: the log is
tamper-evident, the same structure git and blockchains use. Fields are
length-framed before hashing so no two different inputs can collide by
concatenation.

The commit's own time and author metadata are deliberately excluded from the
hash — the ID names the content, and metadata can never perturb the chain
(`Authors` is derived from the changes and cross-checked by `VerifyChain`).
Each `Change`'s `at`/`actor` fields ARE part of that content, though, and
`Append` stamps `at` at staging time — so identical edits staged at different
times still hash differently. Idempotent dedup is powered by idempotency keys,
not hash equality (below).

## Content-addressed storage

An object's identifier is derived from its content rather than assigned: equal
`(parent, message, changes)` always name the same commit, and an ID can be
re-verified from content alone. Write-time dedup is NOT hash-based, though —
`Seal` restamps change timestamps into the payload, and the anti-fork
constraint rejects even a byte-identical re-append (`ErrParentConflict`). A
redelivered message dedups to one commit through its idempotency key
(`Service.Seal` + the `Deduper` capability, [`core/service.go`](core/service.go)).

## Trusted anchor / checkpoint verification

Full verification ([`core/verify.go`](core/verify.go) `Verify`) refetches and
rehashes the entire history — O(all commits), every run. The incremental form
(`VerifyAfter`) exploits the chain structure: once a prefix has been verified,
its head ID transitively attests everything behind it, so the next run needs
only the commits *after* that anchor — O(commits since the last run). The
caller persists the returned head as the next anchor.

This is a degenerate (and, for a linear history, sufficient) case of a
cryptographic accumulator: the chain is its own accumulator, no Merkle tree
required. The trust contract is explicit — tampering at or before the anchor
is invisible to incremental verification, so the anchor is only as
trustworthy as where it is stored. Anchor in the same database guards against
corruption and casual tampering; against a hostile database owner the anchor
(or a signature over it) must live elsewhere.

*Why not a Merkle tree?* Trees buy O(log N) membership proofs — proving one
arbitrary record to a third party. "Is my linear history intact" doesn't need
that.

## Event sourcing

The storage philosophy: persist the *changes* (events), derive current state
by replaying them. `Reconstruct` in [`kit/view.go`](kit/view.go) is the
replay; `State` is a *projection* (materialized view) of the event stream.
Core deliberately stores only the log — every read model is derived.

## Snapshotting

The classic event-sourcing companion pattern: cache the projection as of a
known commit, then replay only the events after it. The `Snapshotter`
capability ([`core/capability.go`](core/capability.go)) stores one opaque
snapshot per document; `State`, `StateAt`, and `CommitSnapshot` in
[`kit/view.go`](kit/view.go) all use it as a fast path, turning reads from
O(all commits) into O(commits since snapshot). The cost is *amortized*: an
occasional full replay primes the cache, subsequent reads are cheap. The
snapshot is a pure cache — deleting it is always safe, readers fall back to
full replay.

Historical reads (`StateAt`, `CommitSnapshot`) never refresh the snapshot: a
read of the past must not move the HEAD cache.

## Cursor (keyset) pagination

`TailReader.CommitsAfter(docID, afterID, limit)` — "the commits after this
one" — rather than OFFSET-style paging. Keyset cursors stay O(page) at any
depth and are the primitive under both the snapshot fast path and incremental
verification. Adapters implement it with an indexed range scan
([`adapters/sql/capability.go`](adapters/sql/capability.go)).

## Capability pattern (graceful degradation)

Backends implement optional interfaces — `Indexer`, `TailReader`,
`Snapshotter`, `Deduper` ([`core/capability.go`](core/capability.go)) —
discovered by Go type assertion. A backend without a capability, or a stale
or corrupt cache, degrades to the slower correct path (full replay), never to
a wrong answer. This is the Go-idiomatic strategy pattern: behavior selected
at runtime by what the value can do, not by configuration.

## Optimistic concurrency control

Writers append assuming no conflict; the backend detects a stale parent
(`ErrParentConflict`, enforced by `UNIQUE(doc_id, parent)` in the SQL
adapter) and `Service.Seal` retries with full-jitter exponential backoff
([`core/service.go`](core/service.go)). Contrast with pessimistic locking:
nothing is held between read and write, conflicts are resolved by retry.
Per-document writes are still serialized — that is the intended consistency
boundary, not a bug.

## Idempotency keys

At-least-once delivery (queues, webhooks, retrying HTTP clients) is made
exactly-once at the log: `Seal` with `WithIdempotencyKey` consults the
`Deduper` and returns the original commit for a replayed key instead of
sealing a duplicate. Keys are scoped per document.

## Lowest common ancestor (LCA)

`CommitSnapshot` scopes its output to the deepest path prefix shared by all
of a commit's changed paths (`lcaPath`, [`kit/view.go`](kit/view.go)) — the
smallest subtree that contains the whole diff. Segment-wise longest common
prefix, climbing to the nearest enclosing container when the LCA is a scalar.

## Prior art

Systems and paradigms chronicle borrows from — useful when explaining what it
is, or looking up how a problem was solved elsewhere. Ranked by closeness.

- **git** — the write model: stage, seal into a content-addressed commit,
  hash-chain to the parent. See [the git model](core/MODEL.md) for the
  side-by-side and the one deliberate divergence (commit metadata stays out of
  the hash).
  One inversion worth knowing: git stores snapshots and derives diffs;
  chronicle stores diffs and derives snapshots.
- **Event sourcing / CQRS** (Greg Young; Kafka's log-as-truth) — the
  architecture: the append-only change log is the source of truth, every state
  is a derived projection.
- **Temporal databases** — the read model. Datomic's `as-of` query is exactly
  `StateAt`; XTDB and SQL:2011 temporal tables (`FOR SYSTEM_TIME AS OF`) are
  the same idea. chronicle tracks commit time only (no separate valid-time
  axis, i.e. not fully bitemporal).
- **Verifiable logs** — the integrity model. Certificate Transparency
  (RFC 6962) runs public tamper-evident append-only logs with incremental
  consistency proofs; `VerifyAfter` is the linear-chain version of that idea.
  AWS QLDB, immudb, and Google Trillian are ledger databases in the same
  family. A blockchain minus consensus: one writer, no proof-of-anything.
- **Audit-trail libraries** (Rails PaperTrail, Hibernate Envers, Django
  simple-history) — the product niche: per-record change history with
  who/when/what. chronicle is that niche made storage-agnostic, diff-native,
  and tamper-evident.
- **Write-ahead log (WAL)** — the database-internal ancestor: append-only log
  as truth, tables as projections.
- **Change data capture** (Debezium) — adjacent but inverted: CDC extracts
  changes from a database after the fact; chronicle records them first-class
  at write time.
- **CRDTs / operational transformation** — the deliberate contrast: those
  merge concurrent divergent histories; chronicle forbids divergence outright
  (`ErrFork`, strictly linear per-document chains).
