package chroniclekit

import (
	"context"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
)

// Kit is the one-stop facade over a changelog.Service: produce changes, seal
// them, and reconstruct/render on read. It is adapter-agnostic — the caller
// injects the Service (and thus picks the backend).
type Kit struct {
	svc changelog.Service
}

// New returns a Kit over svc.
func New(svc changelog.Service) *Kit { return &Kit{svc: svc} }

// Service returns the underlying Service (for reads the Kit does not wrap).
func (k *Kit) Service() changelog.Service { return k.svc }

type recordConfig struct {
	message        string
	idempotencyKey string
}

// RecordOption configures a single Record call.
type RecordOption func(*recordConfig)

// WithMessage annotates the sealed commit.
func WithMessage(m string) RecordOption {
	return func(c *recordConfig) { c.message = m }
}

// WithIdempotencyKey makes the seal a dedup-safe replay (forwarded to
// Service.Seal): a retry with the same key returns the original commit.
func WithIdempotencyKey(key string) RecordOption {
	return func(c *recordConfig) { c.idempotencyKey = key }
}

// RecordUpdate diffs before→after and seals the resulting Changes as one commit.
// If nothing changed it returns changelog.ErrEmptyChanges (reusing core's
// sentinel — there is nothing to commit).
func (k *Kit) RecordUpdate(ctx context.Context, docID string, before, after any, opts ...RecordOption) (changelog.Commit, error) {
	changes, err := Diff(before, after)
	if err != nil {
		return changelog.Commit{}, err
	}
	return k.RecordChanges(ctx, docID, changes, opts...)
}

// RecordChanges seals pre-built Changes as one commit.
func (k *Kit) RecordChanges(ctx context.Context, docID string, changes []changelog.Change, opts ...RecordOption) (changelog.Commit, error) {
	var cfg recordConfig
	for _, o := range opts {
		o(&cfg)
	}
	var sopts []changelog.SealOption
	if cfg.idempotencyKey != "" {
		sopts = append(sopts, changelog.WithIdempotencyKey(cfg.idempotencyKey))
	}
	return k.svc.Seal(ctx, docID, changes, cfg.message, sopts...)
}
