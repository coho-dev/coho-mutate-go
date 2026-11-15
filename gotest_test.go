package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// The files under testdata/events are what `go test -json` printed for a
// small package, with each test's output left out except the line saying the
// binary timed out.
func readEvents(t *testing.T, name string) TestRun {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "events", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	run, err := readTestRun(f)
	if err != nil {
		t.Fatalf("readTestRun(%s): %v", name, err)
	}
	return run
}

func TestReadTestRun_AllPassing(t *testing.T) {
	run := readEvents(t, "pass.jsonl")
	if got := keysOf(run.Passed); !slices.Equal(got, []string{"TestExit", "TestHang", "TestLater"}) {
		t.Errorf("Passed = %v", got)
	}
	if len(run.Failed) != 0 || len(run.Unfinished) != 0 || run.Ending != EndedPassing {
		t.Errorf("a passing run read as %+v", run)
	}
	if run.Elapsed != 3*time.Millisecond {
		t.Errorf("Elapsed = %v, want the package's 3ms", run.Elapsed)
	}
}

// A test that fails without stopping the binary lets every later test run,
// and the binary prints FAIL itself at its end.
func TestReadTestRun_TestsThatFailWithoutStoppingTheBinary(t *testing.T) {
	run := readEvents(t, "fail.jsonl")
	if run.Ending != EndedFailing {
		t.Errorf("read as %+v, want a failed run that was not stopped", run)
	}
	if got := leaves(run.Failed); !slices.Equal(got, []string{"TestA/sub"}) {
		t.Errorf("failed leaves = %v", got)
	}
}

// A test that panics is reported failed, with its parents, and the binary
// stops, so the tests after it never start.
func TestReadTestRun_APanicFailsTheTestThatPanicked(t *testing.T) {
	run := readEvents(t, "panic.jsonl")
	if got := keysOf(run.Failed); !slices.Equal(got, []string{"TestA", "TestA/sub", "TestPanic", "TestPanic/deep"}) {
		t.Errorf("Failed = %v", got)
	}
	if run.Ending != EndedStopped {
		t.Errorf("read as %+v", run)
	}
	if got := leaves(run.Failed); !slices.Equal(got, []string{"TestA/sub", "TestPanic/deep"}) {
		t.Errorf("failed leaves = %v, want the two subtests and neither parent", got)
	}
	if run.LastStarted != "TestPanic/deep" {
		t.Errorf("LastStarted = %q, want TestPanic/deep, the test the binary stopped in", run.LastStarted)
	}
}

// A test that exits the binary itself never finishes, and it is the one that
// caught the bug.
func TestReadTestRun_ATestTheBinaryStoppedInsideIsUnfinished(t *testing.T) {
	run := readEvents(t, "exit.jsonl")
	if !slices.Equal(run.Unfinished, []string{"TestExit"}) {
		t.Errorf("Unfinished = %v, want [TestExit]", run.Unfinished)
	}
	if run.Ending != EndedStopped {
		t.Errorf("Ending = %q, want stopped", run.Ending)
	}
}

func TestReadTestRun_ATimeoutLeavesTheHangingTestUnfinished(t *testing.T) {
	run := readEvents(t, "timeout.jsonl")
	if run.Ending != EndedTimedOut {
		t.Errorf("Ending = %q, want timed out", run.Ending)
	}
	if !slices.Equal(run.Unfinished, []string{"TestHang"}) {
		t.Errorf("Unfinished = %v, want [TestHang]", run.Unfinished)
	}

}

func TestReadTestRun_ABuildFailure(t *testing.T) {
	run := readEvents(t, "build.jsonl")
	if run.Ending != EndedUnbuilt {
		t.Errorf("read as %+v, want a build failure, not a stopped binary", run)
	}
}

// A parallel test waits paused until the tests before it finish. One still
// paused when the binary stops never ran its body, so it caught nothing.
func TestReadTestRun_APausedTestIsNotUnfinished(t *testing.T) {
	events := strings.Join([]string{
		`{"Action":"run","Test":"TestWaits"}`,
		`{"Action":"pause","Test":"TestWaits"}`,
		`{"Action":"run","Test":"TestResumes"}`,
		`{"Action":"pause","Test":"TestResumes"}`,
		`{"Action":"cont","Test":"TestResumes"}`,
		`{"Action":"run","Test":"TestExits"}`,
		`{"Action":"fail","Elapsed":0.01}`,
	}, "\n")
	run, err := readTestRun(strings.NewReader(events))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(run.Unfinished, []string{"TestExits", "TestResumes"}) {
		t.Errorf("Unfinished = %v, want [TestExits TestResumes]", run.Unfinished)
	}
}

func TestReadTestRun_ASkippedTestFinished(t *testing.T) {
	events := `{"Action":"run","Test":"TestSkips"}` + "\n" + `{"Action":"skip","Test":"TestSkips"}` + "\n" + `{"Action":"pass"}`
	run, err := readTestRun(strings.NewReader(events))
	if err != nil {
		t.Fatal(err)
	}
	if !run.Passed["TestSkips"] || len(run.Unfinished) != 0 {
		t.Errorf("read as %+v", run)
	}
}

// go prints a few lines that are not events, such as a warning from the
// toolchain, and they say nothing about the tests.
func TestReadTestRun_SkipsLinesThatAreNotEvents(t *testing.T) {
	events := "go: downloading example.com/x v1.0.0\n" + `{"Action":"run","Test":"TestA"}` + "\n" + `{"Action":"pass","Test":"TestA"}` + "\n" + `{"Action":"pass"}`
	run, err := readTestRun(strings.NewReader(events))
	if err != nil {
		t.Fatal(err)
	}
	if !run.Passed["TestA"] {
		t.Errorf("read as %+v", run)
	}
}

func TestReadTestRun_RefusesAnEventItCannotRead(t *testing.T) {
	if _, err := readTestRun(strings.NewReader(`{"Action": 3}`)); err == nil {
		t.Fatal("readTestRun accepted an event whose action is a number")
	}
}

func TestTopLevel(t *testing.T) {
	if got := topLevel("TestA/sub/deeper"); got != "TestA" {
		t.Errorf("topLevel = %q", got)
	}
	if got := topLevel("TestB"); got != "TestB" {
		t.Errorf("topLevel = %q", got)
	}
}

// go test matches -run and -skip one level of a test's name at a time, split
// at each slash.
func matchesByLevel(t *testing.T, pattern, name string) bool {
	t.Helper()
	levels := strings.Split(pattern, "/")
	parts := strings.Split(name, "/")
	if len(parts) < len(levels) {
		return false
	}
	for i, level := range levels {
		if !regexp.MustCompile(level).MatchString(parts[i]) {
			return false
		}
	}
	return true
}

func TestAnyOf(t *testing.T) {
	pattern := anyOf([]string{"TestB", "TestA", "Example_x.y"})
	for _, name := range []string{"TestA", "TestB", "Example_x.y"} {
		if !matchesByLevel(t, pattern, name) {
			t.Errorf("%q does not match %s", pattern, name)
		}
	}
	for _, name := range []string{"TestAB", "XTestA", "Example_xzy"} {
		if matchesByLevel(t, pattern, name) {
			t.Errorf("%q matches %s", pattern, name)
		}
	}
	if got := anyOf(nil); got != "" {
		t.Errorf("anyOf(nil) = %q, want empty", got)
	}
}

func TestExactly(t *testing.T) {
	pattern := exactly("TestA/a.b/c")
	if !matchesByLevel(t, pattern, "TestA/a.b/c") {
		t.Errorf("%q does not match its own name", pattern)
	}
	for _, name := range []string{"TestA/axb/c", "TestAB/a.b/c", "TestA/a.b/cd", "TestA/a.b"} {
		if matchesByLevel(t, pattern, name) {
			t.Errorf("%q matches %s", pattern, name)
		}
	}
}

func TestLeaves(t *testing.T) {
	got := leaves(map[string]bool{"TestA": true, "TestA/x": true, "TestA/x/y": true, "TestB": true, "TestAB": true})
	if !slices.Equal(got, []string{"TestA/x/y", "TestAB", "TestB"}) {
		t.Errorf("leaves = %v", got)
	}
}

// A bug that hangs the package while it loads never reaches the testing
// package's own timer, so go test kills the binary a minute later and says so.
func TestReadTestRun_AHangBeforeAnyTestIsATimeout(t *testing.T) {
	events := `{"Action":"start"}` + "\n" +
		`{"Action":"output","Output":"*** Test killed with quit: ran too long (1m1s).\n"}` + "\n" +
		`{"Action":"fail","Elapsed":61}`
	run, err := readTestRun(strings.NewReader(events))
	if err != nil {
		t.Fatal(err)
	}
	if run.Ending != EndedTimedOut {
		t.Errorf("Ending = %q, want timed out", run.Ending)
	}
}

func TestReadTestRun_KeepsTheCompilersErrors(t *testing.T) {
	events := `{"ImportPath":"x [x.test]","Action":"build-output","Output":"./f_test.go:5:37: undefined: Missing\n"}` + "\n" +
		`{"ImportPath":"x [x.test]","Action":"build-fail"}` + "\n" +
		`{"Action":"fail","FailedBuild":"x [x.test]"}`
	run, err := readTestRun(strings.NewReader(events))
	if err != nil {
		t.Fatal(err)
	}
	if run.Ending != EndedUnbuilt || !strings.Contains(run.BuildOutput, "undefined: Missing") {
		t.Errorf("read as %+v", run)
	}
}

// A run go test reports no result for, such as one whose output was cut off,
// has no ending.
func TestReadTestRun_NoResultForThePackageHasNoEnding(t *testing.T) {
	run, err := readTestRun(strings.NewReader(`{"Action":"run","Test":"TestA"}`))
	if err != nil {
		t.Fatal(err)
	}
	if run.Ending != "" {
		t.Errorf("Ending = %q, want none", run.Ending)
	}
}

// A package can print PASS while it loads, before any test runs. Only the
// line after every test has started says the binary ran to its end.
func TestReadTestRun_APassLineBeforeTheTestsSaysNothing(t *testing.T) {
	events := strings.Join([]string{
		`{"Action":"output","Output":"PASS\n"}`,
		`{"Action":"run","Test":"TestA"}`,
		`{"Action":"fail","Elapsed":0.01}`,
	}, "\n")
	run, err := readTestRun(strings.NewReader(events))
	if err != nil {
		t.Fatal(err)
	}
	if run.Ending != EndedStopped {
		t.Errorf("Ending = %q, want stopped: TestA never finished", run.Ending)
	}
}
