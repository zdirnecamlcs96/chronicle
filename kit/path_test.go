package chroniclekit

import (
	"testing"

	"github.com/zdirnecamlcs96/chronicle/kit/internal/docmodel"
)

func TestPointerRoundTrip_DottedKey(t *testing.T) {
	// An object key containing "." must survive kit-path → JSON Pointer → kit-path.
	path := docmodel.JoinPath([]string{"a.b", "c"}) // `a\.b.c`
	ptr := toPointer(path)
	if ptr != "/a.b/c" {
		t.Fatalf("toPointer(%q) = %q, want \"/a.b/c\"", path, ptr)
	}
	if got := fromPointer(ptr); got != path {
		t.Fatalf("fromPointer(%q) = %q, want %q", ptr, got, path)
	}
}
