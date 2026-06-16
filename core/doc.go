// Package changelog is a git-style, database-agnostic audit log. If you know
// git, you know the model:
//
//   - Change   — one line of a commit's diff (the content of an edit).
//   - Recorder — the PORCELAIN, bound to ONE document. Append stages a Change
//                (git add), Pending lists what's staged (git status), Commit
//                seals the staged Changes into a hash-chained Commit (git commit).
//   - Commit   — an immutable, content-addressed commit: ID = hash(parent,
//                message, changes), chained to its parent. Deliberately UNLIKE
//                git, the hash excludes author + time, so identical content
//                converges to one ID (this powers Deduper).
//   - Log      — the REPOSITORY, where commits live. Each document has its own
//                chain, like a branch: Head is the tip, Commits is `git log`.
//                Storage is pluggable; in-memory / SQL / ClickHouse backends live
//                under adapters/. There is no "init" — a document's history begins
//                at its first commit (parent == "").
//
// In short: the Recorder writes (stage → seal), the Log stores. See core/MODEL.md
// for a side-by-side diagram with git.
//
// The core is generic and standard-library only: feed it by calling
// Recorder.Append with any Change you construct (e.g. from CRUD handlers), or
// build a Service facade (NewService) and Seal from your own transport. The
// library ships no HTTP; exposing it over a wire is the consumer's job.
package changelog
