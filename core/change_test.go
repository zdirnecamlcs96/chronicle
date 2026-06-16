package changelog

import (
	"encoding/json"
	"testing"
	"time"
)

func TestChange_JSONRoundTrip(t *testing.T) {
	in := Change{
		At:    time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
		Actor: "alice",
		Path:  "items.k1.qty",
		Kind:  "put",
		To:    "5",
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Change
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.At.Equal(in.At) || out.Actor != in.Actor || out.Path != in.Path ||
		out.Kind != in.Kind || out.From != in.From || out.To != in.To {
		t.Fatalf("round-trip mismatch: %+v vs %+v", out, in)
	}
}

func TestChange_OmitEmptyFromTo(t *testing.T) {
	b, err := json.Marshal(Change{Actor: "a", Path: "p", Kind: "delete"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	if contains(s, `"from"`) || contains(s, `"to"`) {
		t.Fatalf("empty from/to must be omitted: %s", s)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
