package changelogmemory_test

import (
	"testing"

	"github.com/zdirnecamlcs96/chronicle/core"
	"github.com/zdirnecamlcs96/chronicle/core/conformance"
	changelogmemory "github.com/zdirnecamlcs96/chronicle/adapters/memory"
)

// The memory adapter must satisfy the mandatory Log contract. It does NOT run
// RunSerializableAppend: its AppendCommit stores whatever parent the Recorder
// computed, so concurrent same-doc seals can fork — that durability guarantee
// is the job of a real backend (adapters/sql).
func TestMemoryLog_Conformance(t *testing.T) {
	conformance.RunLogConformance(t, func(t *testing.T) (changelog.Log, func()) {
		return changelogmemory.New(), func() {}
	})
}
