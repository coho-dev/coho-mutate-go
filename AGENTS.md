# AGENTS.md — coho-mutate-go

A mutation tester for Go: it plants small bugs in one package, runs the
package's tests against each, and lists every test that caught each bug.

## Build & test

`make check` builds the binary and runs every test. It is the quality gate.

## Layout

One module, the one `go.mod` names, and one package, `main`, at the repo root.
`testdata/` holds small Go packages the tests run the tester on.

## Invariants

- **It is a general tool.** Nothing in it may assume one repository's layout:
  take module and package paths from Go, never from a fixed folder structure.
