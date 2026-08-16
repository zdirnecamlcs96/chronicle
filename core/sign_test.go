package changelog

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"testing"
)

// fakeSigner is a crypto.Signer whose public key is NOT ed25519.PublicKey —
// used to exercise WithSigner's rejection of non-Ed25519 signers.
type fakeSigner struct{}

func (fakeSigner) Public() crypto.PublicKey { return "not-ed25519" }
func (fakeSigner) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errors.New("fakeSigner: should not be called")
}

func TestRecorder_WithSignerRejectsNonEd25519Signer(t *testing.T) {
	ctx := context.Background()
	r := NewRecorder("doc", newMemLog()).WithSigner(fakeSigner{}, "key-1")
	r.Append(put("alice", "x"))
	_, err := r.Commit(ctx)
	if err == nil {
		t.Fatal("want error for non-ed25519 signer, got nil")
	}
	if len(r.Pending()) != 1 {
		t.Fatalf("pending not restored after signing failure: %d pending", len(r.Pending()))
	}
}

func TestVerifySignatures_RoundTripAllValid(t *testing.T) {
	ctx := context.Background()
	log := newMemLog()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	rec := NewRecorder("doc", log).WithSigner(priv, "key-1")
	for i := 0; i < 3; i++ {
		rec.Append(put("a", "x"))
		if _, err := rec.Commit(ctx); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}

	resolve := func(keyID string) (ed25519.PublicKey, bool) {
		if keyID == "key-1" {
			return pub, true
		}
		return nil, false
	}
	report, err := VerifySignatures(ctx, log, "doc", resolve)
	if err != nil {
		t.Fatal(err)
	}
	if report.Total != 3 || report.Valid != 3 || report.Unsigned != 0 || report.Invalid != 0 || report.UnknownKey != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestVerifySignatures_MixedChainUnsignedNotInvalid(t *testing.T) {
	ctx := context.Background()
	log := newMemLog()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	plain := NewRecorder("doc", log)
	plain.Append(put("a", "x"))
	if _, err := plain.Commit(ctx); err != nil {
		t.Fatalf("unsigned commit: %v", err)
	}

	signed := NewRecorder("doc", log).WithSigner(priv, "key-1")
	signed.Append(put("a", "y"))
	if _, err := signed.Commit(ctx); err != nil {
		t.Fatalf("signed commit: %v", err)
	}

	resolve := func(keyID string) (ed25519.PublicKey, bool) {
		if keyID == "key-1" {
			return pub, true
		}
		return nil, false
	}
	report, err := VerifySignatures(ctx, log, "doc", resolve)
	if err != nil {
		t.Fatal(err)
	}
	if report.Total != 2 || report.Unsigned != 1 || report.Valid != 1 || report.Invalid != 0 || report.UnknownKey != 0 {
		t.Fatalf("unsigned commit must be counted Unsigned, not Invalid: %+v", report)
	}
}

func TestVerifySignatures_WrongKeyIsInvalid(t *testing.T) {
	ctx := context.Background()
	log := newMemLog()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	rec := NewRecorder("doc", log).WithSigner(priv, "key-1")
	rec.Append(put("a", "x"))
	c, err := rec.Commit(ctx)
	if err != nil {
		t.Fatal(err)
	}

	resolve := func(string) (ed25519.PublicKey, bool) { return otherPub, true }
	report, err := VerifySignatures(ctx, log, "doc", resolve)
	if err != nil {
		t.Fatal(err)
	}
	if report.Invalid != 1 || len(report.InvalidIDs) != 1 || report.InvalidIDs[0] != c.ID {
		t.Fatalf("want 1 invalid commit (%s), got %+v", c.ID, report)
	}
}

func TestVerifySignatures_UnresolvableKeyID(t *testing.T) {
	ctx := context.Background()
	log := newMemLog()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	rec := NewRecorder("doc", log).WithSigner(priv, "key-1")
	rec.Append(put("a", "x"))
	c, err := rec.Commit(ctx)
	if err != nil {
		t.Fatal(err)
	}

	resolve := func(string) (ed25519.PublicKey, bool) { return nil, false }
	report, err := VerifySignatures(ctx, log, "doc", resolve)
	if err != nil {
		t.Fatal(err)
	}
	if report.UnknownKey != 1 || len(report.UnknownKeyIDs) != 1 || report.UnknownKeyIDs[0] != c.ID {
		t.Fatalf("want 1 unknown-key commit (%s), got %+v", c.ID, report)
	}
}

func TestVerifySignatures_TamperedSignatureIsInvalid(t *testing.T) {
	ctx := context.Background()
	log := newMemLog()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	rec := NewRecorder("doc", log).WithSigner(priv, "key-1")
	rec.Append(put("a", "x"))
	c, err := rec.Commit(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Tamper the stored signature bytes directly, simulating an attacker with
	// DB write access.
	stored := log.commits["doc"]
	tampered := append([]byte(nil), c.Signature...)
	tampered[0] ^= 0xFF
	stored[len(stored)-1].Signature = tampered

	resolve := func(string) (ed25519.PublicKey, bool) { return pub, true }
	report, err := VerifySignatures(ctx, log, "doc", resolve)
	if err != nil {
		t.Fatal(err)
	}
	if report.Invalid != 1 {
		t.Fatalf("want 1 invalid commit, got %+v", report)
	}
}

// TestVerifySignatures_TamperedSigKeyIDIsUnknownKey tampers only SigKeyID,
// leaving Signature untouched. The signed message is sigPreimageV1||ID —
// SigKeyID plays no part in it — so retargeting the label to one no key is
// registered under leaves VerifySignatures unable to resolve any key at all,
// reported as UnknownKey rather than Invalid.
func TestVerifySignatures_TamperedSigKeyIDIsUnknownKey(t *testing.T) {
	ctx := context.Background()
	log := newMemLog()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	rec := NewRecorder("doc", log).WithSigner(priv, "key-1")
	rec.Append(put("a", "x"))
	if _, err := rec.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	stored := log.commits["doc"]
	stored[len(stored)-1].SigKeyID = "not-registered"

	resolve := func(keyID string) (ed25519.PublicKey, bool) {
		if keyID == "key-1" {
			return pub, true
		}
		return nil, false
	}
	report, err := VerifySignatures(ctx, log, "doc", resolve)
	if err != nil {
		t.Fatal(err)
	}
	if report.UnknownKey != 1 {
		t.Fatalf("want 1 unknown-key commit, got %+v", report)
	}
}

// TestCommit_SignedFieldsSurviveMemLogRoundTrip confirms SigKeyID/Signature
// come back unchanged from the in-module test Log stub's AppendCommit/
// Commits — a plain struct copy, so this is expected to be automatic.
func TestCommit_SignedFieldsSurviveMemLogRoundTrip(t *testing.T) {
	ctx := context.Background()
	log := newMemLog()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	rec := NewRecorder("doc", log).WithSigner(priv, "key-1")
	rec.Append(put("a", "x"))
	sealed, err := rec.Commit(ctx)
	if err != nil {
		t.Fatal(err)
	}

	got, err := log.Commits(ctx, "doc", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].SigKeyID != sealed.SigKeyID || !bytes.Equal(got[0].Signature, sealed.Signature) {
		t.Fatalf("signature fields did not round-trip: got SigKeyID=%q Signature=%x, want SigKeyID=%q Signature=%x",
			got[0].SigKeyID, got[0].Signature, sealed.SigKeyID, sealed.Signature)
	}
}

// TestSignCommit_Golden pins the exact signature for a fixed seed key and a
// fixed commit ID: Ed25519 (RFC 8032) is deterministic, so this value is
// pinned forever. If this test fails, the signing preimage construction
// changed — bump sigPreimageV1 instead.
func TestSignCommit_Golden(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	priv := ed25519.NewKeyFromSeed(seed)

	sig, err := signCommit(priv, "fixed-commit-id-for-golden-signature-test")
	if err != nil {
		t.Fatal(err)
	}

	want := "1111cc9a7cbe08d1d3944ef667e07862c4040586064488957d95d4cca56a45ffa5a24064a6d8bd200b10e82aaf109243ed447b51c89cb7db5bfaedfe244e2502"
	if got := hex.EncodeToString(sig); got != want {
		t.Fatalf("golden signature mismatch: got %s want %s", got, want)
	}
}
