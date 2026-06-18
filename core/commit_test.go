package changelog

import (
	"crypto/sha256"
	"encoding/binary"
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

func TestComputeID_FieldsAreUnambiguous(t *testing.T) {
	// Without length-framing, parent||message is ambiguous: a root commit
	// (parent="") whose message begins with a real commit id hashes the same
	// bytes as the child commit that has that id as its parent. Framing the
	// fields must keep the (parent, message) split distinct.
	changes := []Change{{Actor: "a", To: "1"}}
	child, _ := computeID("abc", "def", changes)
	rootShadow, _ := computeID("", "abcdef", changes)
	if child == rootShadow {
		t.Fatal("ambiguous preimage: (parent, message) split collides")
	}
}

func TestComputeID_CanonicalPreimageFormat(t *testing.T) {
	// Pin the content-address preimage so it cannot drift accidentally: drift
	// would silently change every commit id and break stored chains. Each field
	// is framed with an 8-byte big-endian length prefix.
	changes := []Change{{Actor: "a", Path: "x", Kind: "put", To: "1"}}
	got, _ := computeID("parent-X", "msg", changes)

	payload, _ := json.Marshal(changes)
	h := sha256.New()
	writeFramed := func(b []byte) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		h.Write(n[:])
		h.Write(b)
	}
	writeFramed([]byte("parent-X"))
	writeFramed([]byte("msg"))
	writeFramed(payload)
	want := hex.EncodeToString(h.Sum(nil))

	if got != want {
		t.Fatalf("preimage format drift: got %s want %s", got, want)
	}
}

func TestDistinctAuthors(t *testing.T) {
	got := distinctAuthors([]Change{{Actor: "bob"}, {Actor: "alice"}, {Actor: "bob"}})
	if !reflect.DeepEqual(got, []string{"alice", "bob"}) {
		t.Fatalf("got %v, want [alice bob]", got)
	}
}
