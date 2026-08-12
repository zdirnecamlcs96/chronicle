package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
	chroniclekit "github.com/zdirnecamlcs96/chronicle/kit"
	"github.com/zdirnecamlcs96/chronicle/kit/internal/memlog"
)

// readableRowView mirrors changeRow's readable fields for assertions.
type readableRowView struct {
	CommitID      string          `json:"commit_id"`
	Path          string          `json:"path"`
	Readable      json.RawMessage `json:"readable"`
	ReadableError string          `json:"readable_error"`
}

func sealVia(t *testing.T, h http.Handler, body string) changelog.Commit {
	t.Helper()
	rec := do(h, "POST", "/commits", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /commits = %d (%s)", rec.Code, rec.Body)
	}
	var c changelog.Commit
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// A server opted into WithReadable freezes schema.names at write time; the
// change feed then serves the sidecar rows without any schema input.
func TestChanges_ReadableFromWriteTimeNames(t *testing.T) {
	svc := changelog.NewService(memlog.New())
	h := Handler(svc, chroniclekit.WithReadable())

	c := sealVia(t, h, `{"doc_id":"d1","after":{"owner":"u1"},"schema":{"names":{"u1":"Old Owner"}}}`)

	rec := do(h, "GET", "/changes?doc=d1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /changes = %d (%s)", rec.Code, rec.Body)
	}
	var rows []readableRowView
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].CommitID != c.ID {
		t.Fatalf("rows: %+v", rows)
	}
	var rr struct {
		Display struct{ From, To string } `json:"display"`
	}
	if rows[0].Readable == nil {
		t.Fatalf("row carries no readable: %+v", rows[0])
	}
	if err := json.Unmarshal(rows[0].Readable, &rr); err != nil {
		t.Fatal(err)
	}
	if rr.Display.To != "Old Owner" {
		t.Fatalf("frozen display = %+v, want Old Owner", rr)
	}
}

// Commits without a sidecar serve plain rows — no readable, no error — even
// when others on the document have one. Reads surface sidecars regardless of
// the server's WithReadable opt-in.
func TestChanges_MixedSidecarPresence(t *testing.T) {
	log := memlog.New()
	svc := changelog.NewService(log)
	writer := Handler(svc, chroniclekit.WithReadable())
	reader := Handler(svc) // not opted into writes; reads still decorate

	withSidecar := sealVia(t, writer, `{"doc_id":"d1","after":{"owner":"u1"},"schema":{"names":{"u1":"Old"}}}`)
	// Direct changes seal: no before-state, no sidecar.
	plain := sealVia(t, reader, `{"doc_id":"d1","changes":[{"path":"note","kind":"put","to":"\"n\""}]}`)

	rec := do(reader, "GET", "/changes?doc=d1", "")
	var rows []readableRowView
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for _, row := range rows {
		switch row.CommitID {
		case withSidecar.ID:
			if row.Readable == nil || row.ReadableError != "" {
				t.Fatalf("sidecar'd row: %+v", row)
			}
		case plain.ID:
			if row.Readable != nil || row.ReadableError != "" {
				t.Fatalf("plain row must stay plain: %+v", row)
			}
		default:
			t.Fatalf("unknown commit %s", row.CommitID)
		}
	}
}

// An error-stub sidecar surfaces as readable_error on the commit's rows, so
// the gap is visible and acknowledgeable.
func TestChanges_ErrorStubSurfaced(t *testing.T) {
	log := memlog.New()
	svc := changelog.NewService(log)
	h := Handler(svc)

	c := sealVia(t, h, `{"doc_id":"d1","after":{"owner":"u1"}}`)
	if err := log.SaveAnnotation(context.Background(), changelog.Annotation{
		DocID: "d1", CommitID: c.ID, Data: []byte(`{"error":"boom"}`),
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(h, "GET", "/changes?doc=d1", "")
	var rows []readableRowView
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ReadableError != "boom" || rows[0].Readable != nil {
		t.Fatalf("stub row: %+v", rows)
	}
}

// POST /explain overlays stored seal-time names; commits without a sidecar
// decorate live from the request's options.names.
func TestExplain_StoredNamesWin(t *testing.T) {
	svc := changelog.NewService(memlog.New())
	h := Handler(svc, chroniclekit.WithReadable())

	// Pre-feature commit: direct changes seal, no sidecar.
	sealVia(t, h, `{"doc_id":"d1","changes":[{"path":"owner","kind":"create","to":"\"u1\""}]}`)
	// Sidecar'd commit with seal-time names.
	sealVia(t, h, `{"doc_id":"d1","before":{"owner":"u1"},"after":{"owner":"u2"},"schema":{"names":{"u1":"Old","u2":"New"}}}`)

	rec := do(h, "POST", "/explain", `{"doc":"d1","options":{"names":{"u1":"Latest1","u2":"Latest2"}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /explain = %d (%s)", rec.Code, rec.Body)
	}
	var out []struct {
		Changes []struct {
			Display *struct{ From, To string } `json:"display"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("commits = %d, want 2 (newest first)", len(out))
	}
	// out[0] is the newest (sidecar'd) commit: frozen names win.
	if d := out[0].Changes[0].Display; d == nil || d.From != "Old" || d.To != "New" {
		t.Fatalf("sidecar'd commit display: %+v, want frozen Old/New", d)
	}
	// out[1] is the pre-feature commit: live names from the request.
	if d := out[1].Changes[0].Display; d == nil || d.To != "Latest1" {
		t.Fatalf("pre-feature commit display: %+v, want live Latest1", d)
	}
}
