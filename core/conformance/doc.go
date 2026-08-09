// Package conformance is the conformance suite for changelog.Log
// implementations — the executable contract every storage backend must satisfy.
//
// MemoryLog and every durable backend (e.g. a durable adapter (adapters/sql)) run the
// SAME RunLogConformance suite, which is how "works no matter what database" is
// proven rather than asserted. The suite includes ForkAppend: a Log stores the
// parent it is given, so two commits sharing a parent are a recorded fork, not
// an error — concurrency control belongs to the persistence layer or the
// producer, never to the Log contract.
//
// The package imports only changelog and the standard library, so it adds no
// dependency to the core module; backends import it from their own _test.go
// files.
package conformance
