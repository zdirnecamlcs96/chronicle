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
	// would silently change every commit id and break stored chains. The
	// preimage starts with the unframed version tag, then each field is framed
	// with an 8-byte big-endian length prefix.
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
	h.Write([]byte(commitPreimageV1))
	writeFramed([]byte("parent-X"))
	writeFramed([]byte("msg"))
	writeFramed(payload)
	want := hex.EncodeToString(h.Sum(nil))

	if got != want {
		t.Fatalf("preimage format drift: got %s want %s", got, want)
	}
}

// TestComputeID_Golden pins exact commit IDs for fixed inputs. These hashes
// are pinned forever. If this test fails, the preimage encoding changed and
// historical commits are orphaned — bump the format version instead.
func TestComputeID_Golden(t *testing.T) {
	cases := []struct {
		name    string
		parent  string
		message string
		changes []Change
		want    string
	}{
		{
			name:    "root commit, empty parent",
			parent:  "",
			message: "init",
			changes: []Change{{Actor: "a", Path: "x", Kind: "put", To: "1"}},
			want:    "f22339e290aaee9ff2b6b09e242842343ffa0b13451a7e00cd89fc607fbe718e",
		},
		{
			name:    "commit with parent",
			parent:  "abc123",
			message: "fix typo",
			changes: []Change{{Actor: "a", Path: "x", Kind: "put", To: "2"}},
			want:    "cf356a4d1877c59f286ebd65714ce08ef0619af17dfab1ba111a443ed98f0649",
		},
		{
			name:   "multi-change commit",
			parent: "parent-1",
			changes: []Change{
				{Actor: "a", Path: "x", Kind: "put", To: "1"},
				{Actor: "b", Path: "y", Kind: "delete"},
			},
			want: "2ef7808711bd84eb7899f6d3f39be359f0875d33fb2be72bdfb62e325dffccb6",
		},
		{
			name:    "empty message",
			parent:  "p",
			message: "",
			changes: []Change{{Actor: "a", Path: "x", Kind: "put", To: "1"}},
			want:    "a3d11f7be7bad13d54732645abb2fb479f43674936c6b24d1457f6ddd3f05cee",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := computeID(tc.parent, tc.message, tc.changes)
			if err != nil {
				t.Fatalf("computeID: %v", err)
			}
			if got != tc.want {
				t.Fatalf("golden mismatch: got %s want %s", got, tc.want)
			}
		})
	}
}

func TestDistinctAuthors(t *testing.T) {
	got := distinctAuthors([]Change{{Actor: "bob"}, {Actor: "alice"}, {Actor: "bob"}})
	if !reflect.DeepEqual(got, []string{"alice", "bob"}) {
		t.Fatalf("got %v, want [alice bob]", got)
	}
}
