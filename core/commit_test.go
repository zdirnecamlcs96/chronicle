package changelog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

func TestComputeID_Deterministic(t *testing.T) {
	changes := []Change{{Actor: "a", Path: "x", Kind: "put", To: "1"}}
	id1, err := computeID("", "", changes)
	if err != nil {
		t.Fatalf("computeID: %v", err)
	}
	id2, _ := computeID("", "", changes)
	if id1 != id2 {
		t.Fatalf("not deterministic: %s vs %s", id1, id2)
	}
	if len(id1) != 64 {
		t.Fatalf("want 64-char hex sha256, got %d chars", len(id1))
	}
}

func TestComputeID_ParentChangesID(t *testing.T) {
	changes := []Change{{Actor: "a", Path: "x", Kind: "put", To: "1"}}
	idA, _ := computeID("parent-A", "", changes)
	idB, _ := computeID("parent-B", "", changes)
	if idA == idB {
		t.Fatal("different parents must yield different IDs")
	}
}

func TestComputeID_ContentChangesID(t *testing.T) {
	idA, _ := computeID("p", "", []Change{{Actor: "a", To: "1"}})
	idB, _ := computeID("p", "", []Change{{Actor: "a", To: "2"}})
	if idA == idB {
		t.Fatal("different changes must yield different IDs")
	}
}

func TestComputeID_MessagePresenceChangesID(t *testing.T) {
	changes := []Change{{Actor: "a", Path: "x", Kind: "put", To: "1"}}
	idEmpty, _ := computeID("p", "", changes)
	idWith, _ := computeID("p", "fix typo", changes)
	if idEmpty == idWith {
		t.Fatal("message must affect the commit ID")
	}
}

func TestComputeID_EmptyMessageEquivalentToPreMessage(t *testing.T) {
	// Belt-and-braces: empty message writes zero bytes to the hash, so its
	// commit ID must be exactly what computeID would have produced before
	// the Message field existed.
	changes := []Change{{Actor: "a", Path: "x", Kind: "put", To: "1"}}
	got, _ := computeID("parent-X", "", changes)

	payload, _ := json.Marshal(changes)
	h := sha256.New()
	h.Write([]byte("parent-X"))
	h.Write(payload)
	want := hex.EncodeToString(h.Sum(nil))

	if got != want {
		t.Fatalf("empty-message hash drift: got %s want %s", got, want)
	}
}

func TestDistinctAuthors(t *testing.T) {
	got := distinctAuthors([]Change{{Actor: "bob"}, {Actor: "alice"}, {Actor: "bob"}})
	if !reflect.DeepEqual(got, []string{"alice", "bob"}) {
		t.Fatalf("got %v, want [alice bob]", got)
	}
}
