package chroniclekit

import (
	"github.com/zdirnecamlcs96/chronicle/kit/internal/docmodel"
	"testing"
)

// norm JSON-normalizes v for comparison with reconstructed/snapshot values.
func norm(t *testing.T, v any) any {
	t.Helper()
	out, err := docmodel.Normalize(v)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return out
}
