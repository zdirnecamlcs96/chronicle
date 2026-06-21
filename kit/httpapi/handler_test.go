package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
)

// fakeService mocks changelog.Service so handler tests exercise routing/decoding/
// status codes in isolation (no real storage).
type fakeService struct {
	sealCommit    changelog.Commit
	sealErr       error
	sealedDoc     string
	sealedChanges []changelog.Change
	sealedMessage string

	commits []changelog.Commit
	all     []changelog.DocCommit
	getDC   changelog.DocCommit
	getOK   bool
}

func (f *fakeService) Seal(ctx context.Context, docID string, changes []changelog.Change, message string, opts ...changelog.SealOption) (changelog.Commit, error) {
	f.sealedDoc, f.sealedChanges, f.sealedMessage = docID, changes, message
	return f.sealCommit, f.sealErr
}
func (f *fakeService) Commits(ctx context.Context, docID string, limit int) ([]changelog.Commit, error) {
	return f.commits, nil
}
func (f *fakeService) AllCommits(ctx context.Context, limit int) ([]changelog.DocCommit, error) {
	return f.all, nil
}
func (f *fakeService) Get(ctx context.Context, commitID string) (changelog.DocCommit, bool, error) {
	return f.getDC, f.getOK, nil
}

func do(h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPostCommits_Changes(t *testing.T) {
	f := &fakeService{sealCommit: changelog.Commit{ID: "abc"}}
	rec := do(Handler(f), "POST", "/commits",
		`{"doc_id":"d1","changes":[{"path":"status","kind":"put","to":"\"sent\""}],"message":"m"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	if f.sealedDoc != "d1" || len(f.sealedChanges) != 1 || f.sealedChanges[0].Path != "status" {
		t.Fatalf("seal got doc=%q changes=%+v", f.sealedDoc, f.sealedChanges)
	}
	var got changelog.Commit
	if json.Unmarshal(rec.Body.Bytes(), &got); got.ID != "abc" {
		t.Fatalf("body commit id = %q, want abc", got.ID)
	}
}

func TestPostCommits_BeforeAfterDiffs(t *testing.T) {
	f := &fakeService{sealCommit: changelog.Commit{ID: "x"}}
	rec := do(Handler(f), "POST", "/commits",
		`{"doc_id":"d1","before":{"status":"draft"},"after":{"status":"sent"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	if len(f.sealedChanges) != 1 || f.sealedChanges[0].Path != "status" || f.sealedChanges[0].To != `"sent"` {
		t.Fatalf("expected diffed status change, got %+v", f.sealedChanges)
	}
}

func TestPostCommits_Errors(t *testing.T) {
	t.Run("no doc_id", func(t *testing.T) {
		if rec := do(Handler(&fakeService{}), "POST", "/commits", `{"changes":[]}`); rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
	})
	t.Run("nothing provided", func(t *testing.T) {
		if rec := do(Handler(&fakeService{}), "POST", "/commits", `{"doc_id":"d"}`); rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
	})
	t.Run("empty diff", func(t *testing.T) {
		f := &fakeService{sealErr: changelog.ErrEmptyChanges}
		rec := do(Handler(f), "POST", "/commits", `{"doc_id":"d","before":{"a":1},"after":{"a":1}}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400 for empty diff, got %d (%s)", rec.Code, rec.Body)
		}
	})
}

func TestPostCommits_BeforeOnlyRejected(t *testing.T) {
	// before-only would silently seal a delete-everything commit — must 400.
	f := &fakeService{sealCommit: changelog.Commit{ID: "x"}}
	rec := do(Handler(f), "POST", "/commits", `{"doc_id":"d","before":{"a":1}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("before-only want 400, got %d (%s)", rec.Code, rec.Body)
	}
}

func TestPostCommits_AfterOnlyCreates(t *testing.T) {
	// after-only is a legitimate create (before = empty).
	f := &fakeService{sealCommit: changelog.Commit{ID: "x"}}
	rec := do(Handler(f), "POST", "/commits", `{"doc_id":"d","after":{"a":1}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("after-only create want 201, got %d (%s)", rec.Code, rec.Body)
	}
	if len(f.sealedChanges) != 1 || f.sealedChanges[0].Kind != "create" {
		t.Fatalf("want one create change, got %+v", f.sealedChanges)
	}
}

func TestResponses_SnakeCase(t *testing.T) {
	f := &fakeService{getOK: true, getDC: changelog.DocCommit{DocID: "d", Commit: changelog.Commit{ID: "c1"}}}
	rec := do(Handler(f), "GET", "/commits/c1", "")
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["doc_id"] != "d" {
		t.Fatalf("want snake_case doc_id, got keys %v", body)
	}
	if _, ok := body["DocID"]; ok {
		t.Fatalf("PascalCase DocID leaked: %v", body)
	}
}

func TestGetCommit(t *testing.T) {
	found := &fakeService{getOK: true, getDC: changelog.DocCommit{DocID: "d", Commit: changelog.Commit{ID: "c1"}}}
	if rec := do(Handler(found), "GET", "/commits/c1", ""); rec.Code != http.StatusOK {
		t.Fatalf("found want 200, got %d", rec.Code)
	}
	if rec := do(Handler(&fakeService{getOK: false}), "GET", "/commits/missing", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing want 404, got %d", rec.Code)
	}
}

func TestListCommits(t *testing.T) {
	f := &fakeService{
		commits: []changelog.Commit{{ID: "c1"}},
		all:     []changelog.DocCommit{{DocID: "d", Commit: changelog.Commit{ID: "c1"}}},
	}
	h := Handler(f)
	var perDoc []changelog.Commit
	rec := do(h, "GET", "/commits?doc=d", "")
	json.Unmarshal(rec.Body.Bytes(), &perDoc)
	if rec.Code != http.StatusOK || len(perDoc) != 1 {
		t.Fatalf("per-doc: code=%d n=%d", rec.Code, len(perDoc))
	}
	var allDocs []changelog.DocCommit
	rec = do(h, "GET", "/commits", "")
	json.Unmarshal(rec.Body.Bytes(), &allDocs)
	if rec.Code != http.StatusOK || len(allDocs) != 1 {
		t.Fatalf("all-docs: code=%d n=%d", rec.Code, len(allDocs))
	}
}

func TestListChanges(t *testing.T) {
	f := &fakeService{commits: []changelog.Commit{
		{ID: "c1", Changes: []changelog.Change{{Path: "a", Kind: "put"}, {Path: "b", Kind: "put"}}},
	}}
	rec := do(Handler(f), "GET", "/changes?doc=d", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var rows []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &rows)
	if len(rows) != 2 || rows[0]["commit_id"] != "c1" || rows[0]["path"] != "a" {
		t.Fatalf("flattened feed wrong: %v", rows)
	}
}
