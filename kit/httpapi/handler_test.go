package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
	chroniclekit "github.com/zdirnecamlcs96/chronicle/kit"
	chronicleexplain "github.com/zdirnecamlcs96/chronicle/kit/explain"
	"github.com/zdirnecamlcs96/chronicle/kit/internal/memlog"
	chronicleschema "github.com/zdirnecamlcs96/chronicle/kit/schema"
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

// seedExplainDoc records two revisions of a keyed-array document through the
// real kit, so the explain tests run against a genuine hash-chained history
// rather than hand-built commits.
func seedExplainDoc(t *testing.T) changelog.Service {
	t.Helper()
	svc := memlog.NewService()
	k := chroniclekit.NewWithService(svc)
	opts := chroniclekit.WithDiffOptions(
		chronicleschema.WithArrayKeys(map[string]string{"lines": "product._id"}),
		chronicleschema.WithIdentityFields("_id"),
	)
	v1 := map[string]any{
		"status": "open",
		"meta":   map[string]any{"rev": 1},
		"lines": []any{
			map[string]any{"product": map[string]any{"_id": "P1", "name": "NP1"}, "qty": 1},
		},
	}
	v2 := map[string]any{
		"status": "open",
		"meta":   map[string]any{"rev": 2},
		"lines": []any{
			map[string]any{"product": map[string]any{"_id": "P1", "name": "NP1"}, "qty": 3},
		},
	}
	if _, err := k.RecordUpdate(context.Background(), "order-7", nil, v1, opts); err != nil {
		t.Fatal(err)
	}
	if _, err := k.RecordUpdate(context.Background(), "order-7", v1, v2, opts); err != nil {
		t.Fatal(err)
	}
	return svc
}

const explainBody = `{"doc":"order-7","options":{
	"array_keys":{"lines":"product._id"},
	"identity_fields":["_id"],
	"name_fields":["name"],
	"ignored_fields":["meta.rev"]}}`

func TestPostExplain(t *testing.T) {
	rec := do(Handler(seedExplainDoc(t)), "POST", "/explain", explainBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var got []explainedCommit
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d commits, want 2", len(got))
	}
	// Newest-first, like every other read route: the root commit has no parent.
	if got[0].Parent == "" || got[1].Parent != "" {
		t.Fatalf("not newest-first: %q then %q", got[0].Parent, got[1].Parent)
	}
	// The qty edit is decorated: element name and label trail come from replay,
	// and the raw dotted Path — the client's i18n key — survives on the row.
	var qty *chronicleexplain.Explained
	for i, ch := range got[0].Changes {
		if strings.HasSuffix(ch.Path, "qty") {
			qty = &got[0].Changes[i]
		}
	}
	if qty == nil {
		t.Fatalf("no qty change in %+v", got[0].Changes)
	}
	if qty.Element == nil || qty.Element.Name != "NP1" {
		t.Fatalf("element = %+v, want name NP1", qty.Element)
	}
	if len(qty.Field) == 0 || qty.Field[len(qty.Field)-1] != "Qty" {
		t.Fatalf("field = %v, want trailing Qty", qty.Field)
	}
	if qty.Path == "" {
		t.Fatal("Path missing — clients translate i18n labels from it")
	}
	if qty.From != "1" || qty.To != "3" {
		t.Fatalf("from/to = %q/%q, want 1/3", qty.From, qty.To)
	}
	// ignored_fields only flags; it never hides a recorded change.
	var rev *chronicleexplain.Explained
	for i, ch := range got[0].Changes {
		if strings.HasPrefix(ch.Path, "meta.rev") {
			rev = &got[0].Changes[i]
		}
	}
	if rev == nil || !rev.Bookkeeping {
		t.Fatalf("meta.rev = %+v, want present and Bookkeeping", rev)
	}
}

// The response is snake_case like the other routes, and rows keep the embedded
// Change's own field names.
func TestPostExplain_SnakeCaseKeys(t *testing.T) {
	rec := do(Handler(seedExplainDoc(t)), "POST", "/explain", explainBody)
	body := rec.Body.String()
	for _, key := range []string{`"bookkeeping":`, `"element":`, `"field":`, `"path":`, `"authors":`} {
		if !strings.Contains(body, key) {
			t.Fatalf("body missing %s: %s", key, body)
		}
	}
	if strings.Contains(body, `"Bookkeeping":`) || strings.Contains(body, `"FromValue":`) {
		t.Fatalf("body has Go-cased keys: %s", body)
	}
}

// limit trims the RESPONSE; the replay behind it still starts at the root.
// If limit truncated the history, the element name (which only the first
// commit introduces) would come back empty.
func TestPostExplain_LimitTrimsResponseNotReplay(t *testing.T) {
	rec := do(Handler(seedExplainDoc(t)), "POST", "/explain",
		`{"doc":"order-7","limit":1,"options":{"array_keys":{"lines":"product._id"},"name_fields":["name"]}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var got []explainedCommit
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d commits, want 1", len(got))
	}
	for _, ch := range got[0].Changes {
		if strings.HasSuffix(ch.Path, "qty") {
			if ch.Element == nil || ch.Element.Name != "NP1" {
				t.Fatalf("element = %+v — replay was truncated by limit", ch.Element)
			}
			return
		}
	}
	t.Fatal("no qty change found")
}

func TestPostExplain_NoOptionsStillRenders(t *testing.T) {
	rec := do(Handler(seedExplainDoc(t)), "POST", "/explain", `{"doc":"order-7"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var got []explainedCommit
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d commits, want 2", len(got))
	}
}

func TestPostExplain_Errors(t *testing.T) {
	h := Handler(seedExplainDoc(t))
	if rec := do(h, "POST", "/explain", `{"limit":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing doc: status = %d, want 400", rec.Code)
	}
	if rec := do(h, "POST", "/explain", `{`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON: status = %d, want 400", rec.Code)
	}
}

// The patch route goes through Kit.RecordPatch, so the sealed changes are a
// real diff against stored state — not the forward-only shell ToChanges
// produces. From is the observable proof.
func TestPostCommits_Patch(t *testing.T) {
	svc := memlog.NewService()
	k := chroniclekit.NewWithService(svc)
	if _, err := k.RecordUpdate(context.Background(), "d1", nil, map[string]any{
		"status": "draft",
		"lines":  []any{map[string]any{"id": "a", "qty": 1}},
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(Handler(svc), "POST", "/commits", `{"doc_id":"d1","patch":[
		{"op":"replace","path":"/status","value":"open"},
		{"op":"replace","path":"/lines/0/qty","value":5}
	],"message":"patched"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body)
	}

	var commit changelog.Commit
	if err := json.Unmarshal(rec.Body.Bytes(), &commit); err != nil {
		t.Fatal(err)
	}
	if len(commit.Changes) != 2 {
		t.Fatalf("want 2 changes, got %+v", commit.Changes)
	}
	for _, ch := range commit.Changes {
		if ch.From == "" {
			t.Errorf("%s sealed with no From — /explain would render nothing", ch.Path)
		}
	}
	state, err := k.State(context.Background(), "d1")
	if err != nil {
		t.Fatal(err)
	}
	if state["status"] != "open" {
		t.Fatalf("patch not applied: %v", state)
	}
}

// An op ToChanges would skip must fail the request, not seal the survivors.
func TestPostCommits_Patch_UnsupportedOp(t *testing.T) {
	svc := memlog.NewService()
	k := chroniclekit.NewWithService(svc)
	if _, err := k.RecordUpdate(context.Background(), "d1", nil, map[string]any{"a": 1, "b": 2}); err != nil {
		t.Fatal(err)
	}
	head, err := svc.Commits(context.Background(), "d1", 0)
	if err != nil {
		t.Fatal(err)
	}

	for _, body := range []string{
		`{"doc_id":"d1","patch":[{"op":"replace","path":"/a","value":9},{"op":"move","path":"/b"}]}`,
		`{"doc_id":"d1","patch":[{"op":"replace","path":"","value":{"a":9}}]}`, // whole-document replace
	} {
		rec := do(Handler(svc), "POST", "/commits", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body)
		}
	}
	after, err := svc.Commits(context.Background(), "d1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(head) {
		t.Fatalf("a refused patch sealed a commit: %d → %d", len(head), len(after))
	}
}

// WithActor blank-fills Change.Actor (an explicit per-change actor would win,
// but neither write shape here sets one), and the real Service derives the
// commit's Authors from those actors at seal time — so both must be asserted
// against a real service, not fakeService.
func TestPostCommits_Actor(t *testing.T) {
	t.Run("changes shape", func(t *testing.T) {
		svc := memlog.NewService()
		rec := do(Handler(svc), "POST", "/commits",
			`{"doc_id":"d1","changes":[{"path":"status","kind":"put","to":"\"sent\""}],"actor":"alice"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body)
		}
		var commit changelog.Commit
		if err := json.Unmarshal(rec.Body.Bytes(), &commit); err != nil {
			t.Fatal(err)
		}
		if len(commit.Authors) != 1 || commit.Authors[0] != "alice" {
			t.Fatalf("authors = %v, want [alice]", commit.Authors)
		}
		if len(commit.Changes) != 1 || commit.Changes[0].Actor != "alice" {
			t.Fatalf("change actor = %+v, want alice", commit.Changes)
		}
	})

	t.Run("patch shape", func(t *testing.T) {
		svc := memlog.NewService()
		k := chroniclekit.NewWithService(svc)
		if _, err := k.RecordUpdate(context.Background(), "d1", nil, map[string]any{"status": "draft"}); err != nil {
			t.Fatal(err)
		}
		rec := do(Handler(svc), "POST", "/commits",
			`{"doc_id":"d1","patch":[{"op":"replace","path":"/status","value":"open"}],"actor":"bob"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body)
		}
		var commit changelog.Commit
		if err := json.Unmarshal(rec.Body.Bytes(), &commit); err != nil {
			t.Fatal(err)
		}
		if len(commit.Authors) != 1 || commit.Authors[0] != "bob" {
			t.Fatalf("authors = %v, want [bob]", commit.Authors)
		}
		if len(commit.Changes) != 1 || commit.Changes[0].Actor != "bob" {
			t.Fatalf("change actor = %+v, want bob", commit.Changes)
		}
	})

	t.Run("blank actor unchanged", func(t *testing.T) {
		// An omitted/blank actor must not force-fill Change.Actor — same
		// sealed change either way.
		f := &fakeService{sealCommit: changelog.Commit{ID: "abc"}}
		rec := do(Handler(f), "POST", "/commits",
			`{"doc_id":"d1","changes":[{"path":"status","kind":"put","to":"\"sent\""}],"actor":""}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body)
		}
		if len(f.sealedChanges) != 1 || f.sealedChanges[0].Actor != "" {
			t.Fatalf("sealed changes = %+v, want unset actor", f.sealedChanges)
		}
	})
}

func TestGetState(t *testing.T) {
	svc := memlog.NewService()
	k := chroniclekit.NewWithService(svc)
	c1, err := k.RecordUpdate(context.Background(), "d1", nil, map[string]any{"status": "draft"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.RecordUpdate(context.Background(), "d1",
		map[string]any{"status": "draft"}, map[string]any{"status": "open"}); err != nil {
		t.Fatal(err)
	}
	h := Handler(svc)

	t.Run("head", func(t *testing.T) {
		rec := do(h, "GET", "/state?doc=d1", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
		}
		var state map[string]any
		json.Unmarshal(rec.Body.Bytes(), &state)
		if state["status"] != "open" {
			t.Fatalf("state = %v, want status open", state)
		}
	})

	t.Run("at earlier commit", func(t *testing.T) {
		rec := do(h, "GET", "/state?doc=d1&at="+c1.ID, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
		}
		var state map[string]any
		json.Unmarshal(rec.Body.Bytes(), &state)
		if state["status"] != "draft" {
			t.Fatalf("state = %v, want status draft", state)
		}
	})

	t.Run("missing doc", func(t *testing.T) {
		if rec := do(h, "GET", "/state", ""); rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
	})

	t.Run("unknown at", func(t *testing.T) {
		if rec := do(h, "GET", "/state?doc=d1&at=nope", ""); rec.Code != http.StatusNotFound {
			t.Fatalf("want 404, got %d (%s)", rec.Code, rec.Body)
		}
	})
}

func TestGetVerify(t *testing.T) {
	svc := memlog.NewService()
	k := chroniclekit.NewWithService(svc)
	if _, err := k.RecordUpdate(context.Background(), "d1", nil, map[string]any{"status": "draft"}); err != nil {
		t.Fatal(err)
	}
	c2, err := k.RecordUpdate(context.Background(), "d1",
		map[string]any{"status": "draft"}, map[string]any{"status": "open"})
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(svc)

	t.Run("ok", func(t *testing.T) {
		rec := do(h, "GET", "/verify?doc=d1", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
		}
		var got verifyResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !got.OK || got.Commits != 2 || got.Head != c2.ID {
			t.Fatalf("got %+v, want ok=true commits=2 head=%s", got, c2.ID)
		}
	})

	t.Run("empty doc", func(t *testing.T) {
		rec := do(h, "GET", "/verify?doc=nope", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
		}
		var got verifyResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !got.OK || got.Commits != 0 {
			t.Fatalf("got %+v, want ok=true commits=0", got)
		}
	})

	t.Run("missing doc", func(t *testing.T) {
		if rec := do(h, "GET", "/verify", ""); rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
	})
}

// noUnwrapService wraps a Service without exposing Unwrap, forcing
// tailReaderFor to find nothing and commitsAfter to take its fallback branch.
type noUnwrapService struct{ changelog.Service }

// TestListCommits_Pagination runs the same assertions against a Service that
// exposes a TailReader (memlog, through its Unwrap chain) and one wrapped to
// hide it, so the fast path and the fallback are proven to agree.
func TestListCommits_Pagination(t *testing.T) {
	svc := memlog.NewService()
	k := chroniclekit.NewWithService(svc)
	c1, err := k.RecordUpdate(context.Background(), "d1", nil, map[string]any{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := k.RecordUpdate(context.Background(), "d1", map[string]any{"n": 1}, map[string]any{"n": 2})
	if err != nil {
		t.Fatal(err)
	}
	c3, err := k.RecordUpdate(context.Background(), "d1", map[string]any{"n": 2}, map[string]any{"n": 3})
	if err != nil {
		t.Fatal(err)
	}

	run := func(t *testing.T, h http.Handler) {
		t.Run("after first returns rest oldest-first", func(t *testing.T) {
			rec := do(h, "GET", "/commits?doc=d1&after="+c1.ID, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
			}
			var got []changelog.Commit
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 || got[0].ID != c2.ID || got[1].ID != c3.ID {
				t.Fatalf("got %+v, want [%s %s] oldest-first", got, c2.ID, c3.ID)
			}
		})

		t.Run("limit respected", func(t *testing.T) {
			rec := do(h, "GET", "/commits?doc=d1&after="+c1.ID+"&limit=1", "")
			var got []changelog.Commit
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].ID != c2.ID {
				t.Fatalf("got %+v, want [%s]", got, c2.ID)
			}
		})

		t.Run("unknown after", func(t *testing.T) {
			if rec := do(h, "GET", "/commits?doc=d1&after=nope", ""); rec.Code != http.StatusNotFound {
				t.Fatalf("want 404, got %d (%s)", rec.Code, rec.Body)
			}
		})

		t.Run("after without doc", func(t *testing.T) {
			if rec := do(h, "GET", "/commits?after="+c1.ID, ""); rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d", rec.Code)
			}
		})
	}

	t.Run("TailReader path", func(t *testing.T) { run(t, Handler(svc)) })
	t.Run("fallback path", func(t *testing.T) { run(t, Handler(noUnwrapService{svc})) })
}

// Identity cannot be declared after the fact, so a producer writing over HTTP
// has to be able to declare it in the request that writes.
func TestPostCommits_Schema(t *testing.T) {
	body := func(schema string) string {
		return `{"doc_id":"d1","before":{"lines":[{"id":"a","qty":1},{"id":"b","qty":2}]},` +
			`"after":{"lines":[{"id":"b","qty":2},{"id":"a","qty":1}]},"schema":` + schema + `}`
	}

	// Declared identity: a reorder is not a change, so there is nothing to commit.
	rec := do(Handler(memlog.NewService()), "POST", "/commits", body(`{"identity_fields":["id"]}`))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "nothing to commit") {
		t.Fatalf("keyed pairing must make a reorder empty: %d %s", rec.Code, rec.Body)
	}

	// No identity declared: positional pairing records the reorder as edits.
	rec = do(Handler(memlog.NewService()), "POST", "/commits", body(`{}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body)
	}

	// Strict mode turns that silent positional fall back into a 400.
	rec = do(Handler(memlog.NewService()), "POST", "/commits", body(`{"strict_identity":true}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "identity") {
		t.Errorf("error must say what is wrong: %s", rec.Body)
	}
}
