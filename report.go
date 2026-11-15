package main

import (
	"slices"
	"strings"
)

// The report's shape is the open mutation-testing report, schema version 2,
// as mutation-testing-report-schema 3.9.0 publishes it.
// testdata/mutation-testing-report-schema.json is that release's copy, and
// the tests hold every report to it.

// Report is one mutation-testing report.
type Report struct {
	SchemaVersion string                `json:"schemaVersion"`
	Thresholds    Thresholds            `json:"thresholds"`
	ProjectRoot   string                `json:"projectRoot"`
	Files         map[string]FileResult `json:"files"`
	TestFiles     map[string]TestFile   `json:"testFiles"`
	Performance   Performance           `json:"performance"`
	Framework     Framework             `json:"framework"`
}

// Thresholds are the mutation scores, in percent, the report's viewers color
// as good and as bad.
type Thresholds struct {
	High int `json:"high"`
	Low  int `json:"low"`
}

// FileResult is one mutated file, keyed in the report by its path relative to
// the project root.
type FileResult struct {
	Language string         `json:"language"`
	Source   string         `json:"source"`
	Mutants  []MutantResult `json:"mutants"`
}

// Status is a planted bug's result, in the report's vocabulary.
type Status string

const (
	Killed   Status = "Killed"
	Survived Status = "Survived"
	// NoCoverage is a bug in code no test runs, so no test ran against it.
	NoCoverage Status = "NoCoverage"
	Timeout    Status = "Timeout"
	// RuntimeError is a bug that made the test binary fail with no test to
	// blame.
	RuntimeError Status = "RuntimeError"
	// Ignored is a bug a marker in the source skips, never run.
	Ignored Status = "Ignored"
	// CompileError is a bug that does not compile. The report leaves these
	// out.
	CompileError Status = "CompileError"
)

// MutantResult is one planted bug's result.
type MutantResult struct {
	ID          string   `json:"id"`
	MutatorName string   `json:"mutatorName"`
	Replacement string   `json:"replacement"`
	Location    Location `json:"location"`
	Status      Status   `json:"status"`
	// KilledBy is every test that caught the bug, by its full name. It can
	// miss some only where StatusReason says the search was cut short.
	KilledBy []string `json:"killedBy,omitempty"`
	// StatusReason says why the status is what it is, where that needs
	// saying.
	StatusReason string `json:"statusReason,omitempty"`
	// Duration is the tests' own time against the bug, in milliseconds.
	Duration float64 `json:"duration"`
}

// Location is the span a bug replaced, its end exclusive.
type Location struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Position is a line and column, both counted from one, the column in
// characters.
type Position struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

// TestFile is one test file and the tests it holds, keyed in the report by
// its path relative to the project root.
type TestFile struct {
	Tests []TestDefinition `json:"tests"`
}

// TestDefinition is one test. Its id and its name are both its full name, as
// `go test -run` takes it.
type TestDefinition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Performance is each phase's time, in milliseconds.
type Performance struct {
	Setup      float64 `json:"setup"`
	InitialRun float64 `json:"initialRun"`
	Mutation   float64 `json:"mutation"`
}

// Framework names the tester that wrote the report.
type Framework struct {
	Name string `json:"name"`
}

// Count is how many of the report's bugs have the status.
func (r Report) Count(s Status) int {
	n := 0
	for _, f := range r.Files {
		for _, m := range f.Mutants {
			if m.Status == s {
				n++
			}
		}
	}
	return n
}

func sortTests(tests []TestDefinition) {
	slices.SortFunc(tests, func(a, b TestDefinition) int { return strings.Compare(a.ID, b.ID) })
}
