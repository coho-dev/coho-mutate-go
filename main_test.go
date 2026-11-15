package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"testing"
)

func TestRun_RefusesACommandLineItCannotRead(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"only-a-package"},
		{"a", "b", "c"},
		{"-workers", "0", "a", "b"},
		{"-unknown", "a", "b"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("run(%q) = %d, want 2", args, code)
		}
		if !strings.Contains(stderr.String(), "usage: coho-mutate-go") {
			t.Errorf("run(%q) printed no usage: %q", args, stderr.String())
		}
	}
}

func TestMutate_TheCommandWritesTheReportAndSaysWhatItFound(t *testing.T) {
	t.Setenv("GOWORK", "off")
	reportPath := filepath.Join(t.TempDir(), "report.json")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-workers", "2", "testdata/catch", reportPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("run = %d: %s", code, stderr.String())
	}
	want := `^planted 3 bugs in testdata/catch with 2 workers
  left out 1 that did not compile
  2 in the report: 2 killed, 0 timed out, 0 survived, 0 no test reaches, 0 crashed the tests with no test to blame, 0 skipped by a marker
took \d+(\.\d)?m?s \(the tests alone, before any bug: \d+(\.\d)?m?s\)
report written to .*report\.json
$`
	if !regexp.MustCompile(want).MatchString(stdout.String()) {
		t.Errorf("printed:\n%s\nwant it to match:\n%s", stdout.String(), want)
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	// The run's directory is the project root, so paths start at it.
	if _, ok := report.Files["testdata/catch/catch.go"]; !ok {
		t.Errorf("the report's files are %v, want testdata/catch/catch.go", keysOf(report.Files))
	}
}

func TestMutate_TheCommandFailsWhenThePackageCannotBeMutated(t *testing.T) {
	t.Setenv("GOWORK", "off")
	var stdout, stderr bytes.Buffer
	if code := run([]string{filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "r.json")}, &stdout, &stderr); code != 1 {
		t.Fatalf("run = %d, want 1", code)
	}
	if !strings.HasPrefix(stderr.String(), "coho-mutate-go: ") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

// A hang is one more result: the printout names each bug that hung tests
// and the tests that hung, from the report.
func TestPrintResult_ListsEachHang(t *testing.T) {
	result := Result{Planted: 2, Report: Report{Files: map[string]FileResult{
		"core/x/x.go": {Mutants: []MutantResult{
			{MutatorName: "UpdateOperator", Replacement: "--", Status: Timeout, StatusReason: "TestLoop/big hung, so the tests after it were not run against this bug", Location: Location{Start: Position{Line: 12, Column: 3}}},
			{MutatorName: "EqualityOperator", Replacement: "<=", Status: Killed, Location: Location{Start: Position{Line: 4, Column: 9}}},
		}},
	}}}
	var stdout bytes.Buffer
	printResult(&stdout, result, "core/x", "r.json", 1)
	for _, want := range []string{
		"report written to r.json",
		"1 bug made tests hang",
		`core/x/x.go:12 UpdateOperator "--": TestLoop/big hung`,
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("printed:\n%s\nwant it to contain %q", stdout.String(), want)
		}
	}
}

func TestPrintResult_ListsNoHangWhereThereWasNone(t *testing.T) {
	var stdout bytes.Buffer
	printResult(&stdout, Result{Planted: 2}, "core/x", "r.json", 1)
	if strings.Contains(stdout.String(), "hang") {
		t.Errorf("printed a hang where there was none:\n%s", stdout.String())
	}
}

// A bug no test caught is what you look at next, so the printout lists each
// line holding one, once, with every such bug on it.
func TestPrintResult_ListsEachLineHoldingABugNoTestCaught(t *testing.T) {
	// Each file's bugs are listed out of line order, as nothing promises the
	// report lists them in it.
	plant := FileResult{Source: "if a == b {}\nx := a + b - c\nok := y\n", Mutants: []MutantResult{
		{Replacement: "-", Status: NoCoverage, Location: span(2, 8, 9)},
		{Replacement: "!=", Status: Survived, Location: span(1, 6, 8)},
		{Replacement: "<", Status: Survived, Location: span(1, 6, 8)},
		{Replacement: "<=", Status: Killed, Location: span(1, 6, 8)},
		{Replacement: "+", Status: Survived, Location: span(2, 12, 13)},
	}}
	// Every other status is left out, and so is a line holding only those.
	report := FileResult{Source: "if é != b {}\nok := y\n", Mutants: []MutantResult{
		{Replacement: "==", Status: Survived, Location: span(1, 6, 8)},
		{MutatorName: "BooleanLiteral", Replacement: "false", Status: Timeout, StatusReason: "TestX hung", Location: span(2, 7, 8)},
		{Replacement: "true", Status: RuntimeError, Location: span(2, 7, 8)},
		{Replacement: "nil", Status: Ignored, Location: span(2, 7, 8)},
	}}
	result := Result{Planted: 9, Report: Report{Files: map[string]FileResult{"z/report.go": report, "a/plant.go": plant}}}
	var stdout bytes.Buffer
	printResult(&stdout, result, "z", "r.json", 1)
	want := `3 lines hold bugs no test caught:
  a/plant.go:1  == → !=, == → <
  a/plant.go:2  + → - (no test reaches), - → +
  z/report.go:1  != → ==
`
	got := stdout.String()
	if !strings.HasSuffix(got, want) {
		t.Errorf("printed:\n%s\nwant it to end:\n%s", got, want)
	}
	// The hang list keeps its place, before this one.
	if hangs := strings.Index(got, "made tests hang:"); hangs < 0 || hangs > strings.Index(got, want) {
		t.Errorf("printed:\n%s\nwant the hang list before the list of lines", got)
	}
}

func TestPrintResult_ListsNoLineWhereEveryBugWasCaught(t *testing.T) {
	result := Result{Planted: 1, Report: Report{Files: map[string]FileResult{
		"x.go": {Source: "a == b\n", Mutants: []MutantResult{{Replacement: "!=", Status: Killed, Location: span(1, 3, 5)}}},
	}}}
	var stdout bytes.Buffer
	printResult(&stdout, result, "x", "r.json", 1)
	if !strings.HasSuffix(stdout.String(), "report written to r.json\n") {
		t.Errorf("printed a list where every bug was caught:\n%s", stdout.String())
	}
}

// span is a Location on one line, from column start up to column end.
func span(line, start, end int) Location {
	return Location{Start: Position{Line: line, Column: start}, End: Position{Line: line, Column: end}}
}

func TestMutate_TheCommandPlantsOnlyWhereAChangeTouchedWithSince(t *testing.T) {
	dir, base := changedRepo(t, calcFiles, map[string]string{
		"calc.go": "package calc\n\nfunc Add(x, y int) int { return x + y }\nfunc Sub(a, b int) int { return a - b }\nfunc Mul(a, b int) int { return a * b }\n",
	})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-since", base, dir, filepath.Join(t.TempDir(), "r.json")}, &stdout, &stderr); code != 0 {
		t.Fatalf("run = %d: %s", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "planted 1 bugs in ") {
		t.Errorf("printed:\n%s\nwant it to plant Add's one bug alone", stdout.String())
	}
}

func TestRun_VersionPrintsTheVersionWithNoOtherArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(-version) = %d, want 0: %s", code, stderr.String())
	}
	// A test binary carries no module version, which Go writes as (devel).
	if stdout.String() != "(devel)\n" {
		t.Errorf("run(-version) printed %q, want %q", stdout.String(), "(devel)\n")
	}
	if stderr.Len() != 0 {
		t.Errorf("run(-version) wrote to stderr: %q", stderr.String())
	}
}

func TestModuleVersion_IsTheVersionGoRecordedForTheMainModule(t *testing.T) {
	for _, c := range []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{"an installed release", &debug.BuildInfo{Main: debug.Module{Version: "v0.2.0"}}, true, "v0.2.0"},
		{"a build from a checkout", &debug.BuildInfo{Main: debug.Module{Version: "v0.1.1-0.20261113032813-991225303be7+dirty"}}, true, "v0.1.1-0.20261113032813-991225303be7+dirty"},
		{"a build Go stamped no version on", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true, "(devel)"},
		{"a build with an empty version", &debug.BuildInfo{}, true, "(devel)"},
		{"a binary with no build info", nil, false, "(devel)"},
	} {
		if got := moduleVersion(c.info, c.ok); got != c.want {
			t.Errorf("%s: moduleVersion = %q, want %q", c.name, got, c.want)
		}
	}
}
