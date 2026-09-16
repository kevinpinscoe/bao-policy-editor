package main

import "fmt"

// version, commit, and date are populated by a release process via
// -ldflags, e.g.:
//
//	go build -ldflags "-X main.version=v0.1.0 -X main.commit=abc123 -X main.date=2026-09-16T00:00:00Z" ./cmd/bpe
//
// No release process exists yet (FSM-10 deliberately does not add one), so
// a development build keeps these safe, useful defaults.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// versionString formats the version line printed by `bpe --version`.
func versionString() string {
	return fmt.Sprintf("bpe %s (commit %s, built %s)", version, commit, date)
}
