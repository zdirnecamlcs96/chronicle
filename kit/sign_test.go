package chroniclekit

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
	"github.com/zdirnecamlcs96/chronicle/kit/internal/memlog"
)

// noSignerService wraps a Service without exposing WithSigner — embedding by
// interface type drops the concrete WithSigner method the real *service
// carries — so WithSigner's capability check takes its missing-capability
// path.
type noSignerService struct{ changelog.Service }

// preSign configures svc with a signer directly (bypassing WithSigner),
// simulating a caller-built Service that already signs before it reaches
// NewWithService.
func preSign(svc changelog.Service, signer crypto.Signer, keyID string) changelog.Service {
	s, ok := svc.(interface {
		WithSigner(crypto.Signer, string) changelog.Service
	})
	if !ok {
		panic("changelog.Service built by NewService must expose WithSigner")
	}
	return s.WithSigner(signer, keyID)
}

// resolverFor returns a KeyResolver that only resolves keyID.
func resolverFor(keyID string, pub ed25519.PublicKey) changelog.KeyResolver {
	return func(id string) (ed25519.PublicKey, bool) {
		if id == keyID {
			return pub, true
		}
		return nil, false
	}
}

func TestKit_WithSigner_RecordUpdateSigned(t *testing.T) {
	ctx := context.Background()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	inner := memlog.New()
	k := New(inner, WithSigner(priv, "key-1"))

	c, err := k.RecordUpdate(ctx, "doc", nil, map[string]any{"status": "open"})
	if err != nil {
		t.Fatal(err)
	}
	if c.SigKeyID != "key-1" || len(c.Signature) == 0 {
		t.Fatalf("commit not signed: %+v", c)
	}
	report, err := changelog.VerifySignatures(ctx, inner, "doc", resolverFor("key-1", pub))
	if err != nil {
		t.Fatal(err)
	}
	if report.Total != 1 || report.Valid != 1 {
		t.Fatalf("report = %+v, want 1 valid", report)
	}
}

func TestKit_WithSigner_RecordPatchSigned(t *testing.T) {
	ctx := context.Background()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	inner := memlog.New()
	k := New(inner, WithSigner(priv, "key-1"))

	if _, err := k.RecordUpdate(ctx, "doc", nil, map[string]any{"status": "draft"}); err != nil {
		t.Fatal(err)
	}
	c, err := k.RecordPatch(ctx, "doc", []Operation{
		{Op: "replace", Path: "/status", Value: []byte(`"open"`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.SigKeyID != "key-1" || len(c.Signature) == 0 {
		t.Fatalf("patch commit not signed: %+v", c)
	}
	report, err := changelog.VerifySignatures(ctx, inner, "doc", resolverFor("key-1", pub))
	if err != nil {
		t.Fatal(err)
	}
	if report.Total != 2 || report.Valid != 2 {
		t.Fatalf("report = %+v, want 2 valid", report)
	}
}

func TestKit_WithSigner_BaselineSigned(t *testing.T) {
	ctx := context.Background()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	inner := memlog.New()
	k := New(inner, WithSigner(priv, "key-1"))

	before := map[string]any{"status": "draft", "qty": 1}
	after := map[string]any{"status": "sent", "qty": 1}
	if _, err := k.RecordUpdate(ctx, "doc", before, after, WithCaptureBaseline("captured", "importer")); err != nil {
		t.Fatal(err)
	}
	report, err := changelog.VerifySignatures(ctx, inner, "doc", resolverFor("key-1", pub))
	if err != nil {
		t.Fatal(err)
	}
	if report.Total != 2 || report.Valid != 2 {
		t.Fatalf("report = %+v, want baseline + delta both valid", report)
	}
}

func TestKit_UnsignedByDefault(t *testing.T) {
	ctx := context.Background()
	k := New(memlog.New())
	c, err := k.RecordUpdate(ctx, "doc", nil, map[string]any{"status": "open"})
	if err != nil {
		t.Fatal(err)
	}
	if c.SigKeyID != "" || len(c.Signature) != 0 {
		t.Fatalf("expected unsigned commit, got %+v", c)
	}
}

// A Service without the WithSigner capability must fail loudly, not degrade
// to unsigned commits — a signature is authoritative, unlike WithReadable's
// best-effort sidecar.
func TestKit_WithSigner_ServiceWithoutCapabilityFailsLoud(t *testing.T) {
	ctx := context.Background()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	svc := noSignerService{changelog.NewService(memlog.New())}
	k := NewWithService(svc, WithSigner(priv, "key-1"))

	if _, err := k.RecordUpdate(ctx, "doc", nil, map[string]any{"status": "open"}); !errors.Is(err, ErrSignerUnsupported) {
		t.Fatalf("RecordUpdate err = %v, want ErrSignerUnsupported", err)
	}
	if _, err := k.RecordChanges(ctx, "doc", []changelog.Change{{Path: "p", Kind: KindPut, To: "1"}}); !errors.Is(err, ErrSignerUnsupported) {
		t.Fatalf("RecordChanges err = %v, want ErrSignerUnsupported", err)
	}
}

// WithSigner overrides a signer already configured on a Service passed to
// NewWithService — last-option-wins, the precedent WithParent/
// WithCaptureBaseline already use elsewhere in this package.
func TestKit_WithSigner_OverridesExistingServiceSigner(t *testing.T) {
	ctx := context.Background()
	_, oldPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	newPub, newPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	inner := memlog.New()
	svc := preSign(changelog.NewService(inner), oldPriv, "old-key")
	k := NewWithService(svc, WithSigner(newPriv, "new-key"))

	c, err := k.RecordUpdate(ctx, "doc", nil, map[string]any{"status": "open"})
	if err != nil {
		t.Fatal(err)
	}
	if c.SigKeyID != "new-key" {
		t.Fatalf("SigKeyID = %q, want the overriding kit-level key", c.SigKeyID)
	}
	report, err := changelog.VerifySignatures(ctx, inner, "doc", resolverFor("new-key", newPub))
	if err != nil {
		t.Fatal(err)
	}
	if report.Valid != 1 {
		t.Fatalf("report = %+v, want the override key to verify", report)
	}
}
