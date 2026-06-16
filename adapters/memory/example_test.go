package changelogmemory_test

import (
	"context"
	"fmt"

	changelogmemory "github.com/zdirnecamlcs96/chronicle/adapters/memory"
	changelog "github.com/zdirnecamlcs96/chronicle/core"
)

// Example is the Go-native getting-started: stage Changes on a Recorder, seal
// them into a content-addressed Commit, then read the document's history back
// from the Log. The memory adapter is the zero-config reference backend — swap
// in adapters/sql or adapters/clickhouse for durability without touching this
// flow.
func Example() {
	log := changelogmemory.New()

	// A Recorder is the porcelain (staging area + commit) for one document.
	rec := changelog.NewRecorder("invoice-42", log)
	rec.Append(changelog.Change{Actor: "alice", Path: "status", Kind: "put", To: `"open"`})
	rec.Append(changelog.Change{Actor: "alice", Path: "total", Kind: "put", To: "100"})

	commit, err := rec.Commit(context.Background(), changelog.WithMessage("create invoice"))
	if err != nil {
		panic(err)
	}

	history, _ := log.Commits(context.Background(), "invoice-42", 0)
	fmt.Printf("commits=%d message=%q authors=%v changes=%d\n",
		len(history), commit.Message, commit.Authors, len(commit.Changes))
	// Output:
	// commits=1 message="create invoice" authors=[alice] changes=2
}
