package chroniclekit

import (
	"context"
	"errors"
	"fmt"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
	chroniclediff "github.com/zdirnecamlcs96/chronicle/kit/diff"
	"github.com/zdirnecamlcs96/chronicle/kit/internal/docmodel"
	chronicleschema "github.com/zdirnecamlcs96/chronicle/kit/schema"
	chronicleview "github.com/zdirnecamlcs96/chronicle/kit/view"
)

// ErrUnsupportedOp is returned by RecordPatch for an operation it cannot seal:
// an op outside add/replace/remove, or one targeting the document root. The
// first is skipped by ToChanges, which is right for reading a patch and wrong
// for sealing one — the commit would record an edit the client did not send.
// The second has no representable Change (the kit's root path is "", which
// Reconstruct does not apply). A patch arriving over the wire is caller input,
// so both are a 4xx, not a 5xx.
var ErrUnsupportedOp = errors.New("chroniclekit: unsupported JSON Patch op")

// Kit is the one-stop facade over a changelog backend: produce changes, seal
// them, and reconstruct/render on read. It is adapter-agnostic — the caller
// injects the Log (and thus picks the backend).
type Kit struct {
	svc changelog.Service
	r   *chronicleview.Reader
}

// New returns a Kit over any Log, building the changelog.Service it needs. This
// is the common path: a backend is the only thing a caller must choose.
//
//	log, err := changelogsql.Open(ctx, dsn, changelogsql.WithMigrate(true))
//	k := chroniclekit.New(log)
func New(log changelog.Log) *Kit {
	return NewWithService(changelog.NewService(log))
}

// NewWithService returns a Kit over an existing Service — for callers that
// already hold one, or that wrap it. Its reader detects the backend's optional
// Snapshotter and TailReader capabilities once, here; a Service that exposes
// no Unwrap simply gets full-replay reads.
func NewWithService(svc changelog.Service) *Kit {
	return &Kit{svc: svc, r: chronicleview.New(svc)}
}

// Service returns the underlying Service (for reads the Kit does not wrap).
func (k *Kit) Service() changelog.Service { return k.svc }

// State reconstructs docID's current state at HEAD.
func (k *Kit) State(ctx context.Context, docID string) (map[string]any, error) {
	return k.r.State(ctx, docID)
}

// StateWithHead is State plus the ID of the commit the state was reconstructed
// at ("" for an empty document). Pass that ID to WithParent when recording an
// edit built on this state, so the commit records the snapshot it was actually
// diffed against.
func (k *Kit) StateWithHead(ctx context.Context, docID string) (map[string]any, string, error) {
	return k.r.StateWithHead(ctx, docID)
}

// StateAt reconstructs docID's state as of (and including) commitID.
func (k *Kit) StateAt(ctx context.Context, docID, commitID string) (map[string]any, error) {
	return k.r.StateAt(ctx, docID, commitID)
}

// CommitSnapshot returns the per-commit LCA snapshot: the before-state of the
// smallest subtree containing every change in the commit.
func (k *Kit) CommitSnapshot(ctx context.Context, docID, commitID string) (any, error) {
	return k.r.CommitSnapshot(ctx, docID, commitID)
}

type recordConfig struct {
	message        string
	actor          string
	idempotencyKey string
	diffOpts       []chronicleschema.Option
	parent         string
	hasParent      bool
}

// RecordOption configures a single Record call.
type RecordOption func(*recordConfig)

// WithMessage annotates the sealed commit.
func WithMessage(m string) RecordOption {
	return func(c *recordConfig) { c.message = m }
}

// WithActor attributes every change in this call to actor, which is what makes
// the commit's Authors non-empty. Use it when one person made the whole edit —
// the common case. A commit whose changes have different actors is still
// possible: set Change.Actor per change and seal with RecordChanges.
func WithActor(actor string) RecordOption {
	return func(c *recordConfig) { c.actor = actor }
}

// WithIdempotencyKey makes the seal a dedup-safe replay (forwarded to
// Service.Seal): a retry with the same key returns the original commit.
func WithIdempotencyKey(key string) RecordOption {
	return func(c *recordConfig) { c.idempotencyKey = key }
}

// WithParent records commitID as the sealed commit's parent — the snapshot the
// edit was built against (pair it with StateWithHead, or your own store's
// version). An assertion, not a guard: a parent that is no longer the tip
// records a fork, never an error. Without it the parent defaults to the
// document's head at commit time. RecordPatch overrides it — there the anchor
// is the state it read, by construction.
func WithParent(commitID string) RecordOption {
	return func(c *recordConfig) { c.parent, c.hasParent = commitID, true }
}

// WithDiffOptions forwards schema-declaring options to the Diff inside
// RecordUpdate (array identity, value types).
func WithDiffOptions(opts ...chronicleschema.Option) RecordOption {
	return func(c *recordConfig) { c.diffOpts = append(c.diffOpts, opts...) }
}

// RecordUpdate diffs before→after and seals the resulting Changes as one commit.
// If nothing changed it returns changelog.ErrEmptyChanges (reusing core's
// sentinel — there is nothing to commit).
func (k *Kit) RecordUpdate(ctx context.Context, docID string, before, after any, opts ...RecordOption) (changelog.Commit, error) {
	var cfg recordConfig
	for _, o := range opts {
		o(&cfg)
	}
	changes, err := chroniclediff.Diff(before, after, cfg.diffOpts...)
	if err != nil {
		return changelog.Commit{}, err
	}
	return k.RecordChanges(ctx, docID, changes, opts...)
}

// RecordPatch applies a JSON Patch to docID's state at HEAD and seals the
// resulting edit as one commit, anchored to the head that state was read at:
// the commit's parent IS the snapshot the diff was computed against, so its
// From values are true relative to their recorded base. A writer landing
// concurrently produces a sibling commit — a recorded fork, folded
// last-write-wins at read — never a silent rebase onto state this patch did
// not see.
//
// Prefer it to sealing ToChanges output directly: a patch is forward-only, so
// changes built from one alone carry no From, and a commit sealed that way
// replays correctly but has no before-values for Explain to render. Applying
// first and diffing the result recovers From and pairs array elements by
// declared identity — the patch's own indices are whatever the client happened
// to send.
func (k *Kit) RecordPatch(ctx context.Context, docID string, ops []Operation, opts ...RecordOption) (changelog.Commit, error) {
	// Pre-flight before the state read, so a batch this cannot seal costs one
	// pass and leaves no commit.
	for _, op := range ops {
		if _, ok := kindForOp(op.Op); !ok {
			return changelog.Commit{}, fmt.Errorf("%w: %q", ErrUnsupportedOp, op.Op)
		}
		if fromPointer(op.Path) == "" {
			return changelog.Commit{}, fmt.Errorf("%w: %q targets the document root", ErrUnsupportedOp, op.Path)
		}
	}
	before, base, err := k.StateWithHead(ctx, docID)
	if err != nil {
		return changelog.Commit{}, err
	}
	// Normalize is the deep copy: docmodel.Apply rebinds containers in place, so
	// patching `before` directly would leave the diff comparing the result
	// against itself.
	after, err := docmodel.Normalize(before)
	if err != nil {
		return changelog.Commit{}, err
	}
	for _, c := range ToChanges(ops) {
		if after, err = docmodel.Apply(after, c); err != nil {
			return changelog.Commit{}, err
		}
	}
	// Appended last so it wins over any caller-supplied WithParent: the anchor
	// is the state this patch was applied to, by construction.
	opts = append(opts, WithParent(base))
	return k.RecordUpdate(ctx, docID, before, after, opts...)
}

// RecordChanges seals pre-built Changes as one commit.
func (k *Kit) RecordChanges(ctx context.Context, docID string, changes []changelog.Change, opts ...RecordOption) (changelog.Commit, error) {
	var cfg recordConfig
	for _, o := range opts {
		o(&cfg)
	}
	// Stamped here rather than in RecordUpdate so a caller who built the changes
	// by hand gets the same option. An explicit per-change Actor wins: WithActor
	// is the bulk default, not an override.
	if cfg.actor != "" {
		for i := range changes {
			if changes[i].Actor == "" {
				changes[i].Actor = cfg.actor
			}
		}
	}
	var sopts []changelog.SealOption
	if cfg.idempotencyKey != "" {
		sopts = append(sopts, changelog.WithIdempotencyKey(cfg.idempotencyKey))
	}
	if cfg.hasParent {
		sopts = append(sopts, changelog.WithSealParent(cfg.parent))
	}
	return k.svc.Seal(ctx, docID, changes, cfg.message, sopts...)
}
