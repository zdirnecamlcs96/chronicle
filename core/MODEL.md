# The git mental model

`chronicle` is intentionally git-shaped: you **stage** edits, **seal**
them into an immutable, **content-addressed** commit **hash-chained** to its
parent, and each document keeps its own commit history like a **branch**. If you
know git, you already know the model.

```mermaid
flowchart LR
    subgraph GIT["git"]
        direction TB
        G1["working tree<br/>(edits)"]
        G2["index / staging area"]
        G3["commit<br/>SHA = hash(tree, parent,<br/><b>author, time,</b> message)"]
        G4["branch history<br/>HEAD → parent → …"]
        G5["git log · git show &lt;sha&gt;"]
        G1 -->|git add| G2
        G2 -->|git commit -m| G3
        G3 -->|chained onto HEAD| G4
        G4 --> G5
    end

    subgraph CL["chronicle"]
        direction TB
        C1["Change<br/>(one edit)"]
        C2["pending<br/>(staged)"]
        C3["Commit<br/>ID = hash(parent,<br/>message, changes)<br/><b>(no author/time)</b>"]
        C4["per-doc history = Log<br/>Head → parent → …"]
        C5["Commits · FindByID"]
        C1 -->|Recorder.Append| C2
        C2 -->|"Recorder.Commit(WithMessage)"| C3
        C3 -->|chained onto Head| C4
        C4 --> C5
    end

    G1 -.->|≈| C1
    G2 -.->|≈| C2
    G3 -.->|≈| C3
    G4 -.->|≈| C4
    G5 -.->|≈| C5
```

## Two pieces: porcelain vs repository

Two types do the real work; the other two are the data they move.

- **`Recorder` = the porcelain** (git's `add` / `status` / `commit`), bound to
  **one document**. It *writes*: stage with `Append`, inspect with `Pending`, seal
  with `Commit`.
- **`Log` = the repository** — where commits live, **across documents**. It
  *stores*: `Head` is a document's branch tip, `Commits` is its `git log`.
- **`Change`** is a diff line; **`Commit`** is a commit object — the data the
  Recorder moves into the Log.

Two things git users reach for that **don't** exist here:

- **No `init`.** A document's history simply begins at its first commit
  (`parent == ""`, the root).
- **No working tree / index file.** The Recorder's in-memory staged buffer *is*
  the index, until you `Commit`.

## Mapping

| git | chronicle |
|---|---|
| working tree edit | `Change` (one line of a commit's diff) |
| `git add` → index | `Recorder.Append` → pending |
| `git status` | `Recorder.Pending` |
| `git commit -m` | `Recorder.Commit(WithMessage)` |
| commit SHA | `Commit.ID` (`computeID`) |
| HEAD / branch | `Head` / per-document `Log` |
| non-fast-forward push reject | `ErrParentConflict` |
| `git log` | `Commits` / `Indexer.AllCommits` |
| `git show <sha>` | `Indexer.FindByID` |

## The one deliberate divergence

git's commit SHA folds **author + time** into the hash, so every commit is
unique. The changelog **deliberately leaves them out**:

```
Commit.ID = SHA-256( parent + message + canonicalJSON(changes) )   // not At, not Authors
```

So the same logical change — sealed by a different producer, or at a different
time — yields the **same ID**. That content-addressing is what lets an
at-least-once delivery **dedup to a single commit** (`Deduper`): a retry carrying
a key already seen returns the original commit instead of sealing a duplicate.
