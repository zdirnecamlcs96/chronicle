---
title: Reference
permalink: /documentation/reference/
eyebrow: reference
source: reference.md
summary: >-
  The normative contract — what every exported call accepts, guarantees, and
  returns when it fails. Two halves: what a caller uses, then what a backend
  must implement. Most readers only ever need the first.
---
{%- comment -%}
Assigned up here because relative_url's pipe cannot appear inside a table cell.
{%- endcomment -%}
{%- assign u_started = '/documentation/getting-started/' | relative_url -%}
{%- assign u_kit = '/documentation/kit/' | relative_url -%}
{%- assign u_operations = '/documentation/operations/' | relative_url -%}
{%- assign u_concepts = '/documentation/concepts/' | relative_url -%}
{%- assign u_patches = '/documentation/patches/' | relative_url -%}
{%- assign u_design = '/documentation/design/' | relative_url -%}
{%- assign src = site.repo | append: '/blob/main' -%}

For the guided path, read [getting started]({{ u_started }}) first. This page is
for looking things up.

**It has two halves.** [Part one](#part-one--what-a-caller-uses) is what an
application recording and reading changes touches — the data model, the kit,
`Recorder`, `Service`, verification, errors, the shipped adapters, and the HTTP
layer. [Part two](#part-two--what-a-backend-implements) is the contract a
storage backend must satisfy: the `Log` port, the optional capabilities, and the
conformance suite that decides whether you got it right. If you are using one of
the shipped adapters, part two is not for you.

**Conventions.** *Must* and *never* describe guarantees the library upholds and
a caller may rely on; *may* describes something optional. "The suite" means
`core/conformance`, which is the executable form of the `Log` contract in part
two — a backend that passes it satisfies this page.

**Stability.** v0, experimental. Modules are versioned independently
(`core/vX.Y.Z`, `adapters/sql/vX.Y.Z`, …) and the API may change before v1. The
`Log` port plus the conformance suite is the most stable surface; capability
interfaces and adapters are the least.

**Naming.** The `core` module's package is `changelog`. Kit packages are
`chroniclekit`, `chronicleschema`, `chroniclediff`, `chronicleview`,
`chronicleexplain`; adapters are `changelogmemory`, `changelogsql`,
`changelogclickhouse`.

---

## Part one — what a caller uses

Everything in this half is what an application recording and reading changes
touches. If you are picking a backend off the shelf, you never need part two.

---

## Data model

### `Change`

One recorded edit — a line in a commit's diff.

```go
type Change struct {
    At    time.Time `json:"at"`
    Actor string    `json:"actor"`
    Path  string    `json:"path"`
    Kind  string    `json:"kind"`
    From  string    `json:"from,omitempty"`
    To    string    `json:"to,omitempty"`
}
```

| Field | Contract |
|---|---|
| `At` | When the edit happened. **Always overwritten** by `Recorder.Append` (and therefore by `Service.Seal` and every kit record call) from the recorder's clock. A value you set is discarded. Hashed, as part of `Changes`. |
| `Actor` | The author. The distinct, sorted set of actors in a commit becomes its `Authors`. Not validated; `""` is accepted. |
| `Path` | What was edited. **`core` treats this as an opaque string** and imposes no grammar. The dotted grammar (`items.0.qty`) is `chronicleschema`'s, and only the kit interprets it. |
| `Kind` | The operation label. **`core` fixes no vocabulary.** The kit produces and replays exactly `create`, `put`, `delete`; any other kind is an error to `chronicleview.Reconstruct`. |
| `From` / `To` | Before/after values as strings — canonical JSON when produced by `chroniclediff.Diff`, otherwise whatever the producer chose. `""` means "no value on that side": `From` on a create, `To` on a delete. |

### `Commit`

An immutable, content-addressed bundle of changes chained to its parent.

```go
type Commit struct {
    ID      string    `json:"id"`
    Parent  string    `json:"parent"`
    At      time.Time `json:"at"`
    Authors []string  `json:"authors"`
    Message string    `json:"message,omitempty"`
    Changes []Change  `json:"changes"`
}
```

| Field | Hashed | Contract |
|---|---|---|
| `ID` | — | SHA-256 over `(Parent, Message, JSON-encoded Changes)`, each field length-framed with an 8-byte big-endian prefix so adjacent fields cannot be re-split into a colliding preimage. The encoding is `encoding/json` over the `Change` structs, in declaration order — adapters re-marshal from Go structs, never from stored column text, which is what makes the recomputation sound. |
| `Parent` | yes | The previous commit's `ID`; `""` marks the document's root commit. |
| `At` | **no** | When the commit was sealed. Unauthenticated convenience metadata — the authenticated timeline is each `Change.At`. |
| `Authors` | **no** | Distinct `Change.Actor`s, sorted. Derived, so it is protected by recomputation in `VerifyChain`, not by the hash (`ErrAuthorsMismatch`). |
| `Message` | yes | Optional annotation. Editing it after sealing breaks the chain. |
| `Changes` | yes | The edits sealed, in staged order. |

**What content-addressing does and does not give you.** Equal
`(Parent, Message, Changes)` always yields an equal `ID`. It does not give you
exactly-once: each `Change` carries its own `At`, stamped at staging time and
covered by the hash, so re-staging the same logical edit later produces a
*different* id. Use idempotency keys for delivery deduplication.

### `DocCommit` and `Snapshot`

```go
type DocCommit struct { DocID string; Commit Commit }        // cross-document results self-identify
type Snapshot struct  { DocID, CommitID string; State []byte } // opaque to core
```

`core` never interprets `Snapshot.State`; the layer that wrote it does (the kit
writes JSON document state).

---

## The kit

`chroniclekit` is the facade; `chronicleschema` owns the shared document model;
`chroniclediff`, `chronicleview`, and `chronicleexplain` are the three sides of
the flow. Each imports `chronicleschema` and nothing else of the kit's, so any
one can be used without the others. The whole flow is walked through on
[the kit, end to end]({{ u_kit }}).

### `Kit`

```go
func New(log changelog.Log) *Kit
func NewWithService(svc changelog.Service) *Kit

func (k *Kit) RecordUpdate(ctx, docID string, before, after any, opts ...RecordOption) (changelog.Commit, error)
func (k *Kit) RecordChanges(ctx, docID string, changes []changelog.Change, opts ...RecordOption) (changelog.Commit, error)
func (k *Kit) RecordPatch(ctx, docID string, ops []Operation, opts ...RecordOption) (changelog.Commit, error)
func (k *Kit) State(ctx, docID string) (map[string]any, error)
func (k *Kit) StateAt(ctx, docID, commitID string) (map[string]any, error)
func (k *Kit) CommitSnapshot(ctx, docID, commitID string) (any, error)
func (k *Kit) Service() changelog.Service

func WithMessage(m string) RecordOption
func WithActor(actor string) RecordOption
func WithIdempotencyKey(key string) RecordOption
func WithDiffOptions(opts ...chronicleschema.Option) RecordOption
```

| Call | Contract |
|---|---|
| `New(log)` | Builds the `Service` itself — a caller picks a backend and nothing else. |
| `NewWithService(svc)` | Takes one already built, for a caller holding or wrapping a `Service`. |
| `RecordUpdate` | Diffs `before → after` and seals the result as one commit. Nothing changed ⇒ `ErrEmptyChanges`. |
| `RecordChanges` | Seals pre-built changes as given. No diff, so `WithDiffOptions` is inert. |
| `RecordPatch` | Applies an RFC 6902 patch to the document's state at HEAD, then diffs and seals — see [sealing patches]({{ u_patches }}). |
| `State` / `StateAt` | Current state; `StateAt` reconstructs **as of and including** `commitID`. |
| `CommitSnapshot` | The commit's before-state for the smallest subtree containing every change in it. |
| `Service()` | The underlying `Service`, for reads the `Kit` does not wrap. |

| Option | Contract |
|---|---|
| `WithMessage(m)` | Annotates the sealed commit. Hashed. |
| `WithActor(a)` | Fills a **blank** `Change.Actor` on every change being sealed — this is what makes `Commit.Authors` non-empty. An `Actor` already set wins, so a `RecordChanges` call may mix attributions. Applied in `RecordChanges`, so it reaches hand-built changes too. |
| `WithIdempotencyKey(k)` | Forwarded to `Service.Seal`; a retry with the same key returns the original commit. |
| `WithDiffOptions(…)` | Forwards `chronicleschema` options to the diff inside `RecordUpdate` / `RecordPatch`. |

Either constructor detects the backend's `Snapshotter` and `TailReader` once,
through the `Service`'s `Unwrap()` chain. A `Service` exposing no `Unwrap` gets
full-replay reads.

### `chroniclediff.Diff`

```go
func Diff(before, after any, opts ...chronicleschema.Option) ([]changelog.Change, error)
```

| | Contract |
|---|---|
| Input | Both sides JSON-normalized (marshal then unmarshal), so structs honouring `json` tags and maps diff uniformly. |
| Output | `From`/`To` hold canonical-JSON leaf values; `""` means absent on that side. `Diff(x, x)` returns no changes. |
| Root | Documents must be object- or array-rooted. A scalar root produces one change with an empty `Path`, which `Reconstruct` does not apply. |

**Array pairing** walks a chain, per array, stopping at the first hop that holds:

| Hop | Holds when | Result |
|---|---|---|
| 1. `WithArrayKeys[path]` | every element of **both** sides is an object with a unique scalar at the key path | keyed |
| 2. each `WithIdentityFields` entry, in order | same test, on that field | keyed |
| 3. positional | always | by index |

Scalar and mixed arrays always reach hop 3, so a mid-array insertion reads as a
run of puts plus a tail create. Two consequences are contract, not incidental:

| | Consequence |
|---|---|
| **Keyed pairing trades order for identity** | Reordering alone records nothing, so replay reproduces the element *set* (survivors in before order, additions appended), not the after ordering. Use positional arrays where order is data. |
| **Identity cannot be applied retroactively** | It shapes what is recorded; a later declaration cannot re-key sealed commits. Declare it before the first write and keep it stable. `WithStrictIdentity` makes hop 3 an `ErrNoIdentity` for arrays of objects. |

### `chronicleview`

```go
func Reconstruct(commits []changelog.Commit) (map[string]any, error)
func New(svc changelog.Service) *Reader
```

| | Contract |
|---|---|
| Order | `Reconstruct` takes commits **oldest first**. |
| `create` / `put` | Set the value at their path; intermediate containers are vivified as needed (a numeric segment makes an array). |
| `delete` | Removes the path. A mid-array index deletion shifts later elements left. |
| Any other `Kind` | **An error** — the replay never guesses. |
| Non-object root | An error. |
| `Reader` | Adds accelerated paths: with a `Snapshotter` + `TailReader` backend, serves from the stored snapshot plus a tail replay. Every fast path falls back to a full rebuild rather than failing. |

### `chronicleexplain.Explain`

```go
func Explain(commits []changelog.Commit, opts ...chronicleschema.Option) ([][]Explained, error)
```

| | Contract |
|---|---|
| Order | Takes commits **oldest first** — `slices.Reverse` what `Commits` gave you. |
| Alignment | `rows[i][j]` decorates `commits[i].Changes[j]`, one to one. Each `Explained` embeds the stored `Change` untouched, which is what keeps `path` on every row. |
| Derivation | Everything is derived at read time by replaying the chain. **Nothing display-related is ever stored**, so pre-schema records render like new ones and changing an option re-renders all history without touching a stored byte. |
| JSON | `Explained`, `Element`, `Display`, `ValueNode` carry snake_case tags with `omitempty`; the embedded `Change` inlines. |
| Boundary | **The kit emits structure, never formatting** — slices, not joined strings; names, not sentences; canonical scalars, not prettified text; flags, not decisions. Separators, truncation, pluralization, verbs, colours, and folding belong to the consumer. |

### `chronicleschema` options

One vocabulary, two consumers. Which side an option affects is the thing to get
right:

| Option | Side | Effect |
|---|---|---|
| `WithArrayKeys(map[string]string)` | **write** + read | Element identity per array, keyed by the array's index-free dotted path (`""` for a root array), mapping to a dot-path into each element. |
| `WithIdentityFields(fields ...string)` | **write** + read | Generic identity fields tried in order for arrays `WithArrayKeys` does not name. **No default** — the kit guesses no field names. Replaces any earlier call. |
| `WithStrictIdentity()` | **write** | Makes an array of objects with no usable identity an `ErrNoIdentity` instead of a silent fall back to positional pairing. Off by default; scalar, empty, and keyed arrays pass. |
| `WithValueTypes(vts ...ValueType)` | **write** + read | Object shapes compared and recorded as one canonical scalar instead of field-by-field. Replay yields the scalar, not the object; `Explain` keeps the shape a scalar leaf inside container values. `Canon` must return a JSON scalar — anything else is an `ErrBadCanon`. |
| `WithLabels(func([]string) (string, bool))` | read | Resolves a field's label, used verbatim — the kit never translates. `ok == false` falls back to Title Case of the field name. |
| `WithNameFields(names ...string)` | read | Fields tried in order for an element's display name. **Defaults to `{"name"}`**; calling this replaces the default. |
| `WithNames(map[string]string)` | read | Seeds id→name pairs for entities stored outside the document. Names found in the replayed document **win**. Never recorded; `Diff` ignores it. Appends to earlier calls. |
| `WithIgnoredFields(entries ...string)` | read | Flags bookkeeping fields. A bare name matches at any depth; a dotted path matches that field and its subtree. **Recording is unaffected** — the changelog stays a full data record; `Explain` only marks the change `Bookkeeping` so a display can fold it. Appends to earlier calls. |

Write-side options are load-bearing at record time and cannot be changed
retroactively. Read-side options are free to change at any time and re-render
all history.

### JSON Patch interop

```go
func FromChanges(cs []changelog.Change) []Operation  // changes → RFC 6902 patch
func ToChanges(ops []Operation) []changelog.Change   // RFC 6902 patch → changes
```

| `Kind` | JSON Patch `op` |
|---|---|
| `create` | `add` |
| `put` | `replace` |
| `delete` | `remove` |

| | Contract |
|---|---|
| Ops | Only `add`/`replace`/`remove` are produced or consumed. `move`/`copy`/`test` are **silently skipped** by `ToChanges`. |
| `From` | A patch is forward-only: `FromChanges` drops it, `ToChanges` leaves it `""`. |
| `Path` | Converts between the dotted grammar and RFC 6901 pointers (`items.0.qty` ↔ `/items/0/qty`). A root-level empty-string key does not round-trip. |

**To seal a patch use `RecordPatch`, not `RecordChanges(ToChanges(ops))`** — it
recovers `From`, re-pairs arrays by declared identity, and rejects the ops
`ToChanges` skips. Both are pure functions of their argument, which is why
neither can fill `From`; [sealing patches]({{ u_patches }}) is the full path.

---

## `Recorder`

The staging area plus `git commit`, bound to one document. Safe for concurrent
use.

```go
func NewRecorder(docID string, log Log) *Recorder
func (r *Recorder) WithClock(now func() time.Time) *Recorder
func (r *Recorder) Append(c Change)
func (r *Recorder) Pending() []Change
func (r *Recorder) Commit(ctx context.Context, opts ...CommitOption) (Commit, error)

func WithMessage(s string) CommitOption
```

| Call | Contract |
|---|---|
| `NewRecorder` | Defaults to `time.Now().UTC` for timestamps. |
| `WithClock` | Replaces the clock and returns the recorder, so it chains. A `nil` clock resets to the default. |
| `Append` | Stages one change and **stamps `c.At` from the recorder's clock**, overwriting any value supplied. |
| `Pending` | A copy of the staged, uncommitted changes. |
| `Commit` | Seals everything staged into one commit chained onto the document's current `Head`, and appends it to the `Log`. |

| `Commit` behaviour | |
|---|---|
| Nothing staged | `ErrNothingToCommit` |
| Any error | **The staged changes are restored** — a failed commit loses nothing and the call can be retried |
| `WithParent` set | Used as `Parent` instead of reading `Head` — an assertion, not a guard; a stale parent still commits, as a fork |

When to assert: pass `WithParent` (or `Seal`'s `WithSealParent`, which
forwards to it) whenever the writer knows which snapshot it built against —
typically the head a state read returned; the kit's `RecordPatch` does this
automatically via `StateWithHead`. Omitting it chains onto whatever `Head` is
current at commit time, which is only honest for blind appends that never read
state at all.

---

## `Service`

The in-process facade. No transport, no `net/http` dependency.

```go
func NewService(log Log) Service

type Service interface {
    Seal(ctx context.Context, docID string, changes []Change, message string, opts ...SealOption) (Commit, error)
    Commits(ctx context.Context, docID string, limit int) ([]Commit, error)
    AllCommits(ctx context.Context, limit int) ([]DocCommit, error)
    Get(ctx context.Context, commitID string) (dc DocCommit, ok bool, err error)
}

func WithIdempotencyKey(key string) SealOption
```

`NewService` detects the backend's `Indexer` and `Deduper` **once**, at
construction, following any `Unwrap() Log` chain. Construct it once and reuse it.

**`Seal`** runs these steps in order:

| # | Step |
|---|---|
| 1 | Empty `changes` ⇒ `ErrEmptyChanges`, immediately. |
| 2 | With `WithIdempotencyKey` **and** a `Deduper` backend: a key already sealed for this document returns that commit and stops. No new commit. |
| 3 | Stages every change through a fresh `Recorder` — so **every `At` is restamped** — and commits, passing `message` only when non-empty and forwarding `WithSealParent`'s id (if given) as the `Recorder`'s `WithParent`; otherwise the parent defaults to `Head` at commit time. No conflict check: a stale parent still commits, as a fork. |
| 4 | Records the idempotency key **best-effort** after the commit is durable. A failed `MarkSeen` is not reported and degrades that delivery to at-least-once. |

| Method | Contract |
|---|---|
| `Commits` | Delegates straight to the `Log`. |
| `AllCommits` / `Get` | Delegate to the `Indexer`, and **return empty results with a nil error** when the backend has none — check `ok`, not `err`. |

Two asymmetries with `Recorder`: `Seal` takes `message` as a positional argument
rather than an option, and reports an empty batch as `ErrEmptyChanges` where
`Recorder.Commit` reports `ErrNothingToCommit`.

---

## Verification

```go
func Verify(ctx context.Context, log Log, docID string) error
func VerifyAfter(ctx context.Context, log Log, docID, anchorID string) (head string, err error)
```

| | `Verify` | `VerifyAfter` |
|---|---|---|
| Fetches | the whole document | only commits after the anchor, via `TailReader` |
| Cost | O(all commits) | O(commits since the anchor) |
| Returns | `error` | the new verified head — `anchorID` itself when the tail is empty |
| Requires | any `Log` | a `TailReader` backend; errors without one |

**The anchor trust contract.** Because `Parent` is inside every hash, a verified
anchor transitively attests everything behind it. The flip side: **tampering at
or before the anchor is invisible** to `VerifyAfter`. Run the full `Verify` when
the anchor's own provenance is in doubt.

Errors wrap the offending commit's position and id — match with `errors.Is`.
What "tamper-free" means precisely, and the two `[]Commit` forms these are built
on, are in [part two](#chain-verification-on-fetched-commits).

---

## Errors

| Sentinel | Returned by | When |
|---|---|---|
| `ErrNothingToCommit` | `Recorder.Commit` | nothing staged |
| `ErrEmptyChanges` | `Service.Seal`, and so `Kit.RecordChanges` / `Kit.RecordUpdate` | no changes supplied, or the diff produced none |
| `ErrNoSuchCommit` | `TailReader.CommitsAfter` | `afterID` is not on the document |
| `chroniclediff.ErrNoIdentity` | `Diff`, under `WithStrictIdentity` | an array of objects has no usable element identity and would pair positionally |
| `chroniclediff.ErrBadCanon` | `Diff`, when a `ValueType` matches | `Canon` returned something that is not a JSON scalar — sealing it would make the history unparseable on replay |
| `chroniclekit.ErrUnsupportedOp` | `Kit.RecordPatch` | the patch carries an op outside `add`/`replace`/`remove` |
| `ErrHashMismatch` | the verify family | a commit's content no longer hashes to its id |
| `ErrMissingParent` | the verify family | a commit's parent names a commit that is not in the history — forks and multiple roots are legal, an absent parent is not |
| `ErrAuthorsMismatch` | the verify family | `Authors` is not the recomputed actor set of the changes |

---

## Adapters

```go
changelogmemory.New() *Log
changelogsql.Open(ctx, dsn string, opts ...Option) (*Log, error)
changelogsql.New(db *sql.DB, opts ...Option) *Log
changelogclickhouse.Open(ctx, dsn string, opts ...Option) (*Log, error)
changelogclickhouse.New(db *sql.DB) *Log
```

| | `memory` | `sql` (MySQL) | `clickhouse` |
|---|---|---|---|
| Durable | no | yes | yes |
| Concurrent same-document appends | may fork | may fork | may fork |
| `RunLogConformance` (`ForkAppend` included) | passes | passes | passes |
| Options | — | `WithMigrate`, `WithDialect` | `WithMigrate` |
| Extra methods | — | `Close`, `Migrate`, `PruneSeen` (returns rows deleted) | `Close`, `Migrate`, `PruneSeen` (async delete, no count) |

| Caveat | Applies to |
|---|---|
| **Reference and test only.** In-memory — state does not survive a restart. | `memory` |
| **Requires `parseTime=true` in the DSN** so `DATETIME` scans into `time.Time`. `WithDialect` exists but MySQL is the only dialect today. | `sql` |
| `WithMigrate(true)` runs `CREATE TABLE IF NOT EXISTS` during `Open` and is idempotent. There is **no schema-version table** — breaking schema changes need a hand-written migration out of band ([operations]({{ u_operations }})). | `sql`, `clickhouse` |
| Idempotency records have **no automatic TTL**. Cron `PruneSeen` with a retention longer than your producer's maximum redelivery window. | `sql`, `clickhouse` |

---

## `kit/httpapi`

Optional. The core ships no HTTP; this is one worked transport over a `Service`,
and you can equally write your own.

```go
func Handler(svc changelog.Service) http.Handler
```

| Route | Request | Success |
|---|---|---|
| `POST /commits` | `{doc_id, changes[]?, patch[]?, before?, after?, schema?, message?, actor?, idempotency_key?}` | `201` + the `Commit` |
| `GET /commits?doc=&limit=&after=` | — | `200` + that document's commits, or `[{doc_id, commit}]` across all documents when `doc` is omitted; `after=<commit id>` (requires `doc`) returns the tail strictly after it, oldest-first |
| `GET /commits/{id}` | — | `200` + `{doc_id, commit}` |
| `GET /changes?doc=&limit=` | — | `200` + the flattened feed: `{commit_id, doc_id, …change fields}` |
| `GET /state?doc=&at=` | — | `200` + the reconstructed document state; `at=<commit id>` reconstructs as of that commit instead of HEAD |
| `GET /verify?doc=` | — | `200` + `{ok, commits, head?}` on a clean chain, `{ok:false, commits, error}` on a broken one — a failed verification is a result, not a transport error |
| `POST /explain` | `{doc, limit?, options?}` | `200` + commits newest-first, each `{id, parent, at, authors, message?, changes}` where every change is display-decorated |

`POST /commits` takes one of three write shapes, tried in this order — the
first one present wins, so a body naming two is not an error:

| Field | Method | Meaning |
|---|---|---|
| `changes` | `RecordChanges` | seal these changes as given |
| `patch` | `RecordPatch` | RFC 6902 ops, applied to stored state and diffed |
| `before`/`after` | `RecordUpdate` | diff these two documents |

| Body rule | |
|---|---|
| `before` omitted | "create from empty" |
| `before` without `after` | rejected — it would silently delete the document |
| `patch` | needs no `before`; the handler reads state at HEAD itself, which is what lets the sealed changes carry `From` |

**Schema over the wire.** Each side carries the half of the vocabulary that
survives JSON. Both **skip empty fields**, since `name_fields` and
`identity_fields` replace rather than merge and an empty list would wipe the
default.

| | `POST /commits` → `schema` | `POST /explain` → `options` |
|---|---|---|
| Fields | `array_keys`, `identity_fields`, `strict_identity` | `array_keys`, `identity_fields`, `name_fields`, `ignored_fields`, `names` |
| Reaches | the diff in `patch` and `before`/`after` writes; ignored by `changes` | `Explain` |
| Getting it wrong | **permanent** — identity shapes what is recorded, and no later declaration re-keys it. `strict_identity` turns the omission into a `400` | free — re-request with different options |
| No wire form | `value_types` (`ValueType.Canon` is a Go func) | `labels` (a Go func; falls back to Title Case) |

Two properties of `POST /explain` are contract:

| | |
|---|---|
| **`limit` trims the response, never the replay** | Decoration is derived by replaying from the root, so the handler always fetches the whole chain and slices afterwards. A limit that truncated the replay would return confidently wrong labels, names, and element identity. |
| **Rows embed, never duplicate** | The raw `changes` are not repeated alongside the decorated ones; each decorated row embeds the change it decorates, untouched. `path` is therefore on every row — the stable key a client translates from for its own i18n. |

| Status | When |
|---|---|
| `400` | invalid JSON, missing `doc_id` (or `doc` on `/explain`, `/state`, `/verify`), `after` given without `doc`, none of `changes`/`patch`/`after`, `before` without `after`, an empty diff, a patch op outside `add`/`replace`/`remove` or targeting the document root, or `strict_identity` with an array the declared identity does not cover |
| `404` | `GET /commits/{id}` found nothing, `after` naming an unknown commit, or `at` naming an unknown commit |
| `500` | any backend error — logged server-side, and the response body is a generic `{"error":"internal error"}` so adapter internals are never disclosed |

| Limit | |
|---|---|
| `?limit=` | Parsed with `strconv.Atoi`; absent or unparseable means `0`, which per the `Service` contract returns **all** rows. |
| Request body | Capped at 4 MiB. |
| Path segment | A single segment can vivify an array index up to 65 536; aggregate request size is bounded only by the body limit. |

**The handler ships no authentication, authorization, rate limiting, or
per-caller quotas.** Mount it behind your own middleware, and validate changes
from untrusted producers yourself.

---

## Part two — what a backend implements

This half is the contract a storage backend must satisfy. Skip it unless you are
writing a `Log` — the three shipped adapters already satisfy all of it, and
[getting started]({{ u_started }}) never mentions any of it.

---

## The `Log` port

The mandatory storage contract. Three methods, no more.

```go
type Log interface {
    AppendCommit(ctx context.Context, docID string, c Commit) error
    Commits(ctx context.Context, docID string, limit int) ([]Commit, error)
    Head(ctx context.Context, docID string) (string, error)
}
```

| Method | Guarantees |
|---|---|
| `AppendCommit` | Stores one commit for `docID`, extending its history. `c.Parent` is the writer's assertion of the snapshot it built against — the `Log` stores it verbatim; two commits sharing a parent record a fork, not an error. Concurrency control, if a deployment wants one linear chain, is the persistence layer's or the producer's job, never the `Log`'s. |
| `Commits` | Returns the document's commits **newest first**. `limit <= 0` means all; `limit > 0` means the `limit` most recent. A document with no commits returns an empty slice, never an error. |
| `Head` | Returns the document's tip commit id, or `""` when the document has no commits. `""` is not an error. |

Beyond the per-method table, every implementation **must**:

| Requirement | Asserted by |
|---|---|
| Isolate documents — no method ever returns another document's data | `RunLogConformance` |
| Honour context cancellation | `RunLogConformance` |

The port is deliberately per-document. Cross-document reads, dedup, cursors, and
snapshots are all optional capabilities below — none is required for a backend
to be correct.

---

## Optional capabilities

A backend **may** implement any of these. The `Service` and the kit's reader
detect them with a type assertion, walking any `Unwrap()` chain. **A backend
that implements none is still correct** — it simply loses the corresponding
feature, and the layers above keep no fallback of their own.

```go
type Indexer interface {
    AllCommits(ctx context.Context, limit int) ([]DocCommit, error)
    FindByID(ctx context.Context, commitID string) (dc DocCommit, ok bool, err error)
}
type Deduper interface {
    Seen(ctx context.Context, docID, key string) (c Commit, ok bool, err error)
    MarkSeen(ctx context.Context, docID, key string, c Commit) error
}
type TailReader interface {
    CommitsAfter(ctx context.Context, docID, afterID string, limit int) ([]Commit, error)
}
type Snapshotter interface {
    SaveSnapshot(ctx context.Context, s Snapshot) error
    LoadSnapshot(ctx context.Context, docID string) (s Snapshot, ok bool, err error)
}
```

| Capability | Contract | Absent ⇒ |
|---|---|---|
| `Indexer` | Cross-document reads, newest first, `limit <= 0` = all. `FindByID` searches every document. | `Service.AllCommits` returns an empty slice and `Service.Get` returns `ok == false` — **both with a nil error** |
| `Deduper` | `Seen`/`MarkSeen` are scoped **per document**: the same key on a different `docID` is a distinct delivery. `MarkSeen` is first-writer-wins — a no-op when `(docID, key)` exists. | `WithIdempotencyKey` is silently inert; `Seal` stays at-least-once |
| `TailReader` | Commits strictly **after** `afterID`, **oldest first** (replay order — the opposite of `Commits`). `afterID == ""` means from the root. `limit <= 0` = all. An `afterID` not on the document returns `ErrNoSuchCommit`. | `VerifyAfter` errors; the kit's reader falls back to full replay |
| `Snapshotter` | One snapshot per document, latest write wins. A **pure cache** — deleting stored snapshots is always safe. | the kit's reader falls back to full replay |

All three shipped adapters implement all four: `adapters/sql` and
`adapters/clickhouse` durably across a restart, `adapters/memory` in process
only.

---

## Chain verification on fetched commits

The two forms that take commits you already hold, rather than a `Log`. An
adapter's tests use them; `Verify` and `VerifyAfter` are built on them.

```go
func VerifyChain(commits []Commit) error
func VerifyCommits(commits []Commit) error
```

| | `VerifyChain` | `VerifyCommits` |
|---|---|---|
| Input order | **newest first**, as `Log.Commits` returns | any batch — e.g. the **oldest first** tail `TailReader.CommitsAfter` returns |
| Checks | full ancestry: content hash, authors, and every `Parent` | content only: content hash and authors, per commit in isolation |
| Empty input | valid | valid |

`VerifyCommits` never checks that a parent exists — a fork's parent legitimately
lies outside the batch it was given, so that is an ancestry question only
`VerifyChain` (over the full history) can answer.

"Tamper-free" means all three hold — forks and multiple roots are legal
ancestry, not violations:

| Check | Violation |
|---|---|
| Every `Parent` is `""` or the `ID` of a commit in the batch | `ErrMissingParent` |
| Every `ID` equals the recomputed content hash | `ErrHashMismatch` |
| Every `Authors` equals the recomputed actor set | `ErrAuthorsMismatch` |

---

## Conformance

The executable half of this page. A backend is correct when it passes the
mandatory suite.

```go
type NewLog func(t *testing.T) (changelog.Log, func())

func RunLogConformance(t *testing.T, newLog NewLog)          // mandatory
func RunDeduperConformance(t *testing.T, newLog NewLog)      // if you implement Deduper
func RunTailReaderConformance(t *testing.T, newLog NewLog)   // if you implement TailReader
func RunSnapshotterConformance(t *testing.T, newLog NewLog)  // if you implement Snapshotter
```

| Suite | Asserts |
|---|---|
| `RunLogConformance` | empty head and commits, append then head, parent chaining, the stored chain verifies, newest-first order, `limit`, per-document isolation, two commits sharing a parent both landing with `Head` as the latest arrival (`ForkAppend`), context cancellation |
| `RunDeduperConformance` | idempotency keys are scoped per document: a key marked on one document never resolves on another, and resolves to that other document's own commit once marked there |
| `RunTailReaderConformance` | oldest-first cursor order, `""` from the root, mid-cursor reads, `limit`, empty tail after head, `ErrNoSuchCommit` for an unknown **or foreign** cursor, context cancellation |
| `RunSnapshotterConformance` | absent snapshot reports `ok == false`, round-trip, latest write wins, per-document isolation, context cancellation |

The package imports only `changelog` and the standard library, so depending on it
from your `_test.go` adds nothing to your build.

---

## What this library does not do

Stated plainly, because the git framing invites the assumption. Why each is
absent is on [design decisions]({{ u_design }}#what-is-deliberately-absent).

| Absent | Instead |
|---|---|
| Merge, rebase, cherry-pick | A concurrent append is a recorded fork, not a conflict — both commits land, folded last-write-wins at replay |
| Branches | "Branch" in this model means "document" |
| Transport, client SDK | `core` imports nothing outside the stdlib; `kit/httpapi` is optional, stdlib-only, and has no client library in any language |
| Authentication, authorization | Yours — not in the `Service`, not in the handler |
| Retention policy | Commits are never pruned automatically. Only the `seen` (idempotency) table has a prune helper, and you must schedule it |
| Cross-document transactions | A `Seal` covers one document |

Provided as-is, without warranty. Review it against your own threat model before
exposing it to untrusted input.
