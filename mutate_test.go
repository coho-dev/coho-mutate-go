package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The packages under testdata are modules of their own, outside the repo's
// workspace.
func mutateFixture(t *testing.T, name string) Result {
	t.Helper()
	t.Setenv("GOWORK", "off")
	result, err := Mutate(context.Background(), Options{Dir: "testdata/" + name, Workers: 4, Timeout: time.Second})
	if err != nil {
		t.Fatalf("Mutate(%s): %v", name, err)
	}
	return result
}

// summarize is each bug in a file as a line a test can compare:
// line:column, the kind of bug, its replacement, its status, every test
// that caught it, and why its status is what it is, where the report says.
func summarize(f FileResult) []string {
	var lines []string
	for _, m := range f.Mutants {
		line := fmt.Sprintf("%d:%d %s %q %s %v",
			m.Location.Start.Line, m.Location.Start.Column, m.MutatorName, m.Replacement, m.Status, m.KilledBy)
		if m.StatusReason != "" {
			line += " (" + m.StatusReason + ")"
		}
		lines = append(lines, line)
	}
	return lines
}

func testNames(tf TestFile) []string {
	var names []string
	for _, test := range tf.Tests {
		names = append(names, test.ID)
	}
	return names
}

func TestMutate_ReportsEveryTestThatCaughtEachBug(t *testing.T) {
	result := mutateFixture(t, "catch")
	want := []string{
		`6:35 ArithmeticOperator "-" Killed [TestAdd]`,
		`9:33 BooleanLiteral "" Killed [TestNotFalse TestNotTrue]`,
	}
	if got := summarize(result.Report.Files["catch.go"]); !slices.Equal(got, want) {
		t.Errorf("catch.go's bugs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if result.Planted != 3 || result.LeftOut != 1 {
		t.Errorf("planted %d and left out %d, want 3 and 1: Join's bug does not compile", result.Planted, result.LeftOut)
	}
	tests := testNames(result.Report.TestFiles["catch_test.go"])
	if !slices.Equal(tests, []string{"TestAdd", "TestJoin", "TestNotFalse", "TestNotTrue"}) {
		t.Errorf("catch_test.go's tests = %v, want every test, TestJoin too though it caught nothing", tests)
	}
	if len(result.Report.Files) != 1 || len(result.Report.TestFiles) != 1 {
		t.Errorf("files %v, test files %v", keysOf(result.Report.Files), keysOf(result.Report.TestFiles))
	}
}

// A bug that panics stops the test binary in the first test it reaches, and
// one that hangs stops it at the timeout, so the tests left unrun are run
// again to see whether they catch it too. That includes the other cases of a
// table test whose first case panicked, at any depth.
func TestMutate_CrashesHangsSurvivorsAndCodeNoTestRuns(t *testing.T) {
	result := mutateFixture(t, "crash")
	at := "[TestAtEnd TestAtGroup/group/first TestAtGroup/group/second TestAtLater/empty TestAtTable/one TestAtTable/two]"
	count := "[TestCount TestCountAgain TestCountFourth TestCountThird]"
	want := []string{
		`9:7 EqualityOperator "<=" Killed ` + at,
		`9:7 EqualityOperator ">=" Killed ` + at,
		`17:7 EqualityOperator ">=" Survived []`,
		`17:7 EqualityOperator "<=" Killed [TestMax]`,
		`24:36 EqualityOperator "!=" NoCoverage []`,
		`33:16 EqualityOperator "<=" Killed ` + count,
		`33:16 EqualityOperator ">=" Killed ` + count,
		`34:4 UpdateOperator "--" Killed ` + count,
		`35:4 UpdateOperator "--" Timeout [TestCount] (TestCount hung, so the tests after it were not run against this bug)`,
	}
	if got := summarize(result.Report.Files["crash.go"]); !slices.Equal(got, want) {
		t.Errorf("crash.go's bugs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	tests := testNames(result.Report.TestFiles["crash_test.go"])
	wantTests := []string{"TestAtEnd", "TestAtGroup", "TestAtGroup/group", "TestAtGroup/group/first", "TestAtGroup/group/second",
		"TestAtLater", "TestAtLater/empty", "TestAtTable", "TestAtTable/one", "TestAtTable/two",
		"TestCount", "TestCountAgain", "TestCountFourth", "TestCountThird", "TestMax"}
	if !slices.Equal(tests, wantTests) {
		t.Errorf("crash_test.go's tests = %v", tests)
	}
	// Unjoined's bug is in code no test runs, and does not compile either.
	if result.Planted != 10 || result.LeftOut != 1 {
		t.Errorf("planted %d and left out %d, want 10 and 1", result.Planted, result.LeftOut)
	}
}

// Go's coverage profile gives a case expression no block, so a bug there reads
// as reached exactly when its switch or select statement did.
func TestMutate_ACaseNoTestReachesHasNoCoverage(t *testing.T) {
	result := mutateFixture(t, "cases")
	want := []string{
		`8:15 ArithmeticOperator "-" Killed [TestSize]`,
		`13:9 EqualityOperator ">=" Killed [TestSize]`,
		`13:9 EqualityOperator "<=" Killed [TestSize]`,
		`22:9 EqualityOperator "!=" NoCoverage []`,
		`31:14 ArithmeticOperator "/" NoCoverage []`,
		`32:10 BooleanLiteral "false" NoCoverage []`,
		`34:10 BooleanLiteral "true" NoCoverage []`,
	}
	if got := summarize(result.Report.Files["cases.go"]); !slices.Equal(got, want) {
		t.Errorf("cases.go's bugs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// When a test exits the binary, a parallel test running beside it is cut off
// too. It is credited only if it catches the bug when it runs alone, and then
// the subtests that caught it are credited, not the parent, including the ones
// after a subtest that stops the binary again. A parallel test whose subtest
// panics while another test starts later has the rest of its subtests run too.
func TestMutate_ParallelTestsCutOffByAnother(t *testing.T) {
	result := mutateFixture(t, "parallel")
	want := map[string][]string{
		"parallel.go": {
			`11:7 EqualityOperator "<=" Killed [TestGroup TestZero]`,
			`11:7 EqualityOperator ">=" Killed [TestGroup TestPair/b TestSlow/five TestSlow/six TestZero]`,
		},
		"get.go": {
			`7:7 EqualityOperator "<=" Killed [TestP1/one TestP1/two]`,
			`7:7 EqualityOperator ">=" Killed [TestP1/one TestP1/two TestP2/a]`,
		},
	}
	for file, w := range want {
		if got := summarize(result.Report.Files[file]); !slices.Equal(got, w) {
			t.Errorf("%s's bugs:\n%s\nwant:\n%s", file, strings.Join(got, "\n"), strings.Join(w, "\n"))
		}
	}
}

// A bug a marker skips is reported as Ignored, with the marker's reason, and
// never run: run, this one would hang every test.
func TestMutate_ABugAMarkerSkipsIsIgnored(t *testing.T) {
	result := mutateFixture(t, "ignore")
	want := []string{
		`7:16 EqualityOperator "<=" Killed [TestCount]`,
		`7:16 EqualityOperator ">=" Killed [TestCount]`,
		`8:4 UpdateOperator "--" Killed [TestCount]`,
		`10:4 UpdateOperator "--" Ignored [] (counting i down never ends)`,
	}
	if got := summarize(result.Report.Files["ignore.go"]); !slices.Equal(got, want) {
		t.Errorf("ignore.go's bugs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestMutate_TheReportMatchesTheOpenSchema(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile("testdata/mutation-testing-report-schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []string{"catch", "crash", "parallel", "ignore"} {
		t.Run(fixture, func(t *testing.T) {
			report, err := json.Marshal(mutateFixture(t, fixture).Report)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(report))
			if err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(doc); err != nil {
				t.Errorf("the report does not match the schema: %v\n%s", err, report)
			}
		})
	}
}

func TestMutate_RefusesAPackageWhoseTestsAlreadyFail(t *testing.T) {
	t.Setenv("GOWORK", "off")
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":    "module example.com/failing\n\ngo 1.27.0\n",
		"f.go":      "package failing\n\nfunc One() int { return 1 + 0 }\n",
		"f_test.go": "package failing\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) { t.Fatal(\"broken already\") }\n",
	})
	_, err := Mutate(context.Background(), Options{Dir: dir})
	if err == nil || !strings.Contains(err.Error(), "fail before any bug is planted") {
		t.Fatalf("Mutate = %v, want a refusal naming the failing tests", err)
	}
	if !strings.Contains(err.Error(), "TestOne") {
		t.Errorf("the refusal %q does not name TestOne", err)
	}
}

func TestMutate_RefusesAPackageWhoseTestsDoNotCompile(t *testing.T) {
	t.Setenv("GOWORK", "off")
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":    "module example.com/unbuilt\n\ngo 1.27.0\n",
		"f.go":      "package unbuilt\n\nfunc One() int { return 1 + 0 }\n",
		"f_test.go": "package unbuilt\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) { _ = Missing }\n",
	})
	_, err := Mutate(context.Background(), Options{Dir: dir})
	if err == nil || !strings.Contains(err.Error(), "do not compile") || !strings.Contains(err.Error(), "Missing") {
		t.Fatalf("Mutate = %v, want a refusal saying the tests do not compile, with the compiler's error", err)
	}
}

// A test function named the same in the package and in its external test
// package would be one name in go test's events, and the report could not
// say which caught a bug.
func TestMutate_RefusesATestNamedTheSameInBothTestPackages(t *testing.T) {
	t.Setenv("GOWORK", "off")
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":      "module example.com/twice\n\ngo 1.27.0\n",
		"f.go":        "package twice\n\nfunc One() int { return 1 + 0 }\n",
		"in_test.go":  "package twice\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {}\n",
		"out_test.go": "package twice_test\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {}\n",
	})
	_, err := Mutate(context.Background(), Options{Dir: dir})
	if err == nil || !strings.Contains(err.Error(), "TestOne") || !strings.Contains(err.Error(), "in_test.go") || !strings.Contains(err.Error(), "out_test.go") {
		t.Fatalf("Mutate = %v, want a refusal naming TestOne and both files", err)
	}
}

func TestMutate_RefusesAPackageWithNoTests(t *testing.T) {
	t.Setenv("GOWORK", "off")
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod": "module example.com/untested\n\ngo 1.27.0\n",
		"f.go":   "package untested\n\nfunc One() int { return 1 + 0 }\n",
	})
	if _, err := Mutate(context.Background(), Options{Dir: dir}); err == nil || !strings.Contains(err.Error(), "has no tests") {
		t.Fatalf("Mutate = %v, want a refusal saying there are no tests", err)
	}
}

func TestMutate_RefusesAPackageWhoseTestFilesHoldOnlyHelpers(t *testing.T) {
	t.Setenv("GOWORK", "off")
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":       "module example.com/untested\n\ngo 1.27.0\n",
		"f.go":         "package untested\n\nfunc One() int { return 1 + 0 }\n",
		"help_test.go": "package untested\n\nfunc two() int { return One() + One() }\n",
	})
	if _, err := Mutate(context.Background(), Options{Dir: dir}); err == nil || !strings.Contains(err.Error(), "has no tests") {
		t.Fatalf("Mutate = %v, want a refusal saying there are no tests", err)
	}
}

func TestReason(t *testing.T) {
	cases := []struct {
		c    Catch
		want string
	}{
		{Catch{Status: Killed}, ""},
		{Catch{Status: Timeout, Hung: []string{"TestA"}}, "TestA hung, so the tests after it were not run against this bug"},
		{Catch{Status: Timeout, Hung: []string{"TestA", "TestB"}}, "one of TestA, TestB hung (they were running in parallel), so the tests after it were not run against this bug"},
		{Catch{Status: Timeout}, "the package hung before any test started"},
	}
	for _, c := range cases {
		if got := reason(Mutant{}, &c.c); got != c.want {
			t.Errorf("reason(%+v) = %q, want %q", c.c, got, c.want)
		}
	}
}

// fixture's bugs could only make a test wrong, so none is planted.
// Positive keeps its bugs: another package of the module calls it.
func TestMutate_PlantsNothingInAFunctionOnlyTestsCall(t *testing.T) {
	result := mutateFixture(t, "testonly/internal/fix")
	want := []string{
		`4:38 EqualityOperator ">=" Killed [TestPositive]`,
		`4:38 EqualityOperator "<=" Killed [TestPositive]`,
	}
	if got := summarize(result.Report.Files["fix.go"]); !slices.Equal(got, want) {
		t.Errorf("fix.go's bugs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
