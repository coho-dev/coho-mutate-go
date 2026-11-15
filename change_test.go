package main

import (
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseDiff_ReadsEachHunkAsItsOwnRange(t *testing.T) {
	// Lines 3 and 5 changed and line 4 did not: git writes two hunks with
	// nothing between them, which must not read as lines 3 to 5.
	diff := `diff --git a.go a.go
index 1111111..2222222 100644
--- a.go
+++ a.go
@@ -3 +3 @@ func A() {
-	x := 1
+	x := 2
@@ -5 +5 @@ func A() {
-	y := 1
+	y := 2
`
	got, err := parseDiff([]byte(diff))
	if err != nil {
		t.Fatal(err)
	}
	if !got.lists("a.go") {
		t.Error("a.go is not listed")
	}
	if got.touches("a.go", 4) {
		t.Error("line 4, between the two hunks, reads as touched")
	}
	for _, line := range []int{3, 5} {
		if !got.touches("a.go", line) {
			t.Errorf("line %d reads as untouched", line)
		}
	}
}

func TestParseDiff_ReadsCountsNewFilesAndDeletions(t *testing.T) {
	diff := `diff --git a.go a.go
--- a.go
+++ a.go
@@ -10,2 +10,3 @@
-	old one
-	old two
+	new one
+	new two
+	new three
@@ -20,4 +20,0 @@
-	gone
-	gone
-	gone
-	gone
diff --git new.go new.go
new file mode 100644
--- /dev/null
+++ new.go
@@ -0,0 +1,2 @@
+package p
+++ looks like a header but is an added line
diff --git gone.go gone.go
deleted file mode 100644
--- gone.go
+++ /dev/null
@@ -1 +0,0 @@
-package p
diff --git tail.go tail.go
--- tail.go
+++ tail.go
@@ -7 +7 @@
-}
\ No newline at end of file
+}
diff --git "odd\tname.go" "odd\tname.go"
--- "odd\tname.go"
+++ "odd\tname.go"
@@ -1 +1 @@
-package p
+package q
diff --git my file.go my file.go
--- my file.go	
+++ my file.go	
@@ -2 +2 @@
-x
+y
diff --git sub/b.go sub/b.go
--- sub/b.go
+++ sub/b.go
@@ -1 +1 @@
-package b
+package c
`
	got, err := parseDiff([]byte(diff))
	if err != nil {
		t.Fatal(err)
	}
	// a.go's second hunk only deletes, and gone.go is deleted: neither adds
	// a line, but each file is still one the change touched.
	want := map[string][]int{
		"a.go":         {10, 11, 12},
		"new.go":       {1, 2},
		"gone.go":      nil,
		"tail.go":      {7},
		"odd\tname.go": {1},
		"my file.go":   {2},
		"sub/b.go":     {1},
	}
	if names := listed(got); !slices.Equal(names, slices.Sorted(maps.Keys(want))) {
		t.Errorf("parseDiff lists %q, want %q", names, slices.Sorted(maps.Keys(want)))
	}
	for name, lines := range want {
		if got := touchedLines(got, name); !slices.Equal(got, lines) {
			t.Errorf("%s: touched lines %v, want %v", name, got, lines)
		}
	}
}

func TestParseDiff_RefusesWhatItCannotRead(t *testing.T) {
	for _, diff := range []string{
		"--- a.go\n+++ a.go\n@@ -1 +x @@\n",
		"--- a.go\n+++ a.go\n@@ nonsense\n",
		"@@ -1 +1 @@\n-a\n+b\n",
		"--- a.go\n+++ a.go\n@@ -1 +1,2 @@\n-a\n+b\n",
	} {
		if got, err := parseDiff([]byte(diff)); err == nil {
			t.Errorf("parseDiff(%q) = %v, want an error", diff, got)
		}
	}
}

func TestParseDiff_AnEmptyDiffTouchesNothing(t *testing.T) {
	got, err := parseDiff(nil)
	if err != nil || len(listed(got)) != 0 {
		t.Errorf("parseDiff(nil) = %v, %v, want nothing", got, err)
	}
	if got.touches("a.go", 1) {
		t.Error("an empty change touches a.go")
	}
}

// A file whose hunks only delete is in the report, with no line a bug may go
// on, and a file the diff never names is in neither.
func TestParseDiff_ListsAFileThatOnlyLostLines(t *testing.T) {
	got, err := parseDiff([]byte("--- a.go\n+++ a.go\n@@ -3 +2,0 @@\n-x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.lists("a.go") || got.touches("a.go", 2) || got.touches("a.go", 3) {
		t.Errorf("a.go: lists %v, touches line 2 %v, line 3 %v; want listed with no line touched", got.lists("a.go"), got.touches("a.go", 2), got.touches("a.go", 3))
	}
	if got.lists("b.go") {
		t.Error("b.go, which the diff never names, is listed")
	}
}

// lines is the files a test lists in a scope, with the lines in each.
type lines = map[string][]lineRange

// listed is the names of the files the scope lists, in order.
func listed(s scope) []string {
	var names []string
	for name := range s.files() {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// touchedLines is each of the file's first 30 lines a bug may go on.
func touchedLines(s scope, name string) []int {
	var lines []int
	for line := 1; line <= 30; line++ {
		if s.touches(name, line) {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestWiden_ATestFileTouchesItsSourceFile(t *testing.T) {
	pkg := goPackage{GoFiles: []string{"a.go", "b.go", "c.go"}, TestGoFiles: []string{"a_test.go", "help_test.go"}, XTestGoFiles: []string{"b_test.go"},
		IgnoredGoFiles: []string{"a_windows.go", "a_windows_test.go", "help_windows_test.go"}}
	cases := []struct {
		name    string
		in      lines
		touched []string
	}{
		{"its own test file", lines{"a_test.go": {{4, 4}}}, []string{"a.go"}},
		{"an external test file", lines{"b_test.go": {{1, 1}}}, []string{"b.go"}},
		{"a test file with no source file of its name", lines{"help_test.go": {{1, 1}}}, []string{"a.go", "b.go", "c.go"}},
		{"a test file the change only deleted lines from", lines{"a_test.go": nil}, []string{"a.go"}},
		{"a test file the change deleted", lines{"c_test.go": nil}, []string{"c.go"}},
		{"a source file the change only deleted lines from", lines{"a.go": nil}, nil},
		// Files this machine does not build test nothing that is built here.
		{"a test file for a source file not built here", lines{"a_windows_test.go": {{1, 1}}}, nil},
		{"a test file not built here", lines{"help_windows_test.go": {{1, 1}}}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := newScope()
			for name, ranges := range c.in {
				in.add(name, ranges...)
			}
			got := widen(in, pkg)
			var touched []string
			for _, name := range pkg.GoFiles {
				if got.touches(name, 1) && got.touches(name, 100000) {
					touched = append(touched, name)
				}
			}
			if strings.Join(touched, " ") != strings.Join(c.touched, " ") {
				t.Errorf("wholly touched %v, want %v", touched, c.touched)
			}
		})
	}
}

// changedRepo makes a git repository whose module sits in its sub folder,
// commits files, then commits edits on top. It answers the package's
// directory and the first commit.
func changedRepo(t *testing.T, files, edits map[string]string) (string, string) {
	t.Helper()
	t.Setenv("GOWORK", "off")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	dir := filepath.Join(repo, "sub", "calc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "sub", "go.mod"), []byte("module example.com/sub\n\ngo 1.27.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	writeFiles(t, dir, files)
	git("add", ".")
	git("commit", "-q", "-m", "base")
	base := git("rev-parse", "HEAD")
	writeFiles(t, dir, edits)
	git("commit", "-q", "-am", "change")
	return dir, base
}

// calc's three functions sit on lines 3, 4 and 5, so a change to Add and Mul
// alone is two hunks with nothing between them.
var calcFiles = map[string]string{
	"calc.go": "package calc\n\nfunc Add(a, b int) int { return a + b }\nfunc Sub(a, b int) int { return a - b }\nfunc Mul(a, b int) int { return a * b }\n",
	"calc_test.go": `package calc

import "testing"

func TestCalc(t *testing.T) {
	if Add(2, 3) != 5 || Sub(5, 3) != 2 || Mul(2, 3) != 6 {
		t.Error("wrong")
	}
}
`,
	"neg.go": "package calc\n\nfunc Neg(a int) int {\n\treturn 0 - a\n}\n\nfunc Pos(a int) bool { return a > 0 }\n",
	"neg_test.go": `package calc

import "testing"

func TestNeg(t *testing.T) {
	if Neg(2) != -2 || !Pos(1) || Pos(0) {
		t.Error("wrong")
	}
}
`,
}

func mutateSince(t *testing.T, dir, since string) Result {
	t.Helper()
	result, err := Mutate(context.Background(), Options{Dir: dir, Workers: 2, Timeout: 10 * time.Second, Since: since})
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	return result
}

func TestMutate_SincePlantsOnlyOnTheLinesTheChangeTouched(t *testing.T) {
	dir, base := changedRepo(t, calcFiles, map[string]string{
		"calc.go": "package calc\n\nfunc Add(x, y int) int { return x + y }\nfunc Sub(a, b int) int { return a - b }\nfunc Mul(x, y int) int { return x * y }\n",
	})
	result := mutateSince(t, dir, base)
	want := []string{
		`3:35 ArithmeticOperator "-" Killed [TestCalc]`,
		`5:35 ArithmeticOperator "/" Killed [TestCalc]`,
	}
	if got := summarize(result.Report.Files["calc.go"]); !slices.Equal(got, want) {
		t.Errorf("calc.go's bugs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if files := keysOf(result.Report.Files); !slices.Equal(files, []string{"calc.go"}) {
		t.Errorf("the report's files are %v, want calc.go alone: neg.go is untouched", files)
	}
	if result.Planted != 2 {
		t.Errorf("planted %d bugs, want 2", result.Planted)
	}
}

// A user's diff.interHunkContext would have git join the hunks for Add and
// Mul into one, holding the unchanged Sub between them.
func TestMutate_SinceIgnoresAGitConfigThatJoinsNearbyHunks(t *testing.T) {
	dir, base := changedRepo(t, calcFiles, map[string]string{
		"calc.go": "package calc\n\nfunc Add(x, y int) int { return x + y }\nfunc Sub(a, b int) int { return a - b }\nfunc Mul(x, y int) int { return x * y }\n",
	})
	if out, err := exec.Command("git", "-C", dir, "config", "diff.interHunkContext", "3").CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}
	result := mutateSince(t, dir, base)
	want := []string{
		`3:35 ArithmeticOperator "-" Killed [TestCalc]`,
		`5:35 ArithmeticOperator "/" Killed [TestCalc]`,
	}
	if got := summarize(result.Report.Files["calc.go"]); !slices.Equal(got, want) {
		t.Errorf("calc.go's bugs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestMutate_SinceAChangedTestFilePlantsAcrossItsSourceFile(t *testing.T) {
	dir, base := changedRepo(t, calcFiles, map[string]string{
		"neg_test.go": calcFiles["neg_test.go"] + "\n// One more line.\n",
	})
	result := mutateSince(t, dir, base)
	want := []string{
		`4:11 ArithmeticOperator "+" Killed [TestNeg]`,
		`7:33 EqualityOperator ">=" Killed [TestNeg]`,
		`7:33 EqualityOperator "<=" Killed [TestNeg]`,
	}
	if got := summarize(result.Report.Files["neg.go"]); !slices.Equal(got, want) {
		t.Errorf("neg.go's bugs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if files := keysOf(result.Report.Files); !slices.Equal(files, []string{"neg.go"}) {
		t.Errorf("the report's files are %v, want neg.go alone", files)
	}
}

func TestMutate_SinceAnUntouchedPackagePlantsNothing(t *testing.T) {
	dir, _ := changedRepo(t, calcFiles, map[string]string{"calc.go": calcFiles["calc.go"] + "\n"})
	result := mutateSince(t, dir, "HEAD")
	if result.Planted != 0 || len(result.Report.Files) != 0 || result.Report.SchemaVersion != "2" {
		t.Errorf("planted %d bugs in %v, schema %q; want none, in a report still of schema 2", result.Planted, keysOf(result.Report.Files), result.Report.SchemaVersion)
	}
}

func TestMutate_SinceRefusesARevisionGitCannotRead(t *testing.T) {
	dir, _ := changedRepo(t, calcFiles, map[string]string{"calc.go": calcFiles["calc.go"] + "\n"})
	// A range other than A... ends at a commit, not at the files on disk, so
	// its line numbers need not match the files bugs are planted in.
	for _, since := range []string{"no-such-revision", "--output=/tmp/x", "HEAD~1..HEAD", "HEAD~1...HEAD", "...", "HEAD^!", "HEAD^-"} {
		if _, err := Mutate(context.Background(), Options{Dir: dir, Since: since}); err == nil {
			t.Errorf("Mutate(Since: %q) succeeded, want a refusal", since)
		}
	}
}

// A test file that only lost lines, such as a deleted assertion, can stop
// catching bugs as surely as one that gained them.
func TestMutate_SinceATestFileThatOnlyLostLinesPlantsAcrossItsSourceFile(t *testing.T) {
	dir, base := changedRepo(t, calcFiles, map[string]string{
		"neg_test.go": strings.Replace(calcFiles["neg_test.go"], "\t\tt.Error(\"wrong\")\n", "", 1),
	})
	result := mutateSince(t, dir, base)
	if got := len(result.Report.Files["neg.go"].Mutants); got != 3 {
		t.Errorf("neg.go has %d bugs, want all 3: %v", got, summarize(result.Report.Files["neg.go"]))
	}
}

// With A..., the diff runs from where HEAD split off A to the files on disk,
// so edits not yet committed are counted, and every line number matches the
// file the bugs are planted in.
func TestMutate_SinceFromWhereTheBranchSplitCountsUncommittedEdits(t *testing.T) {
	dir, base := changedRepo(t, calcFiles, map[string]string{
		"calc.go": "package calc\n\nfunc Add(x, y int) int { return x + y }\nfunc Sub(a, b int) int { return a - b }\nfunc Mul(x, y int) int { return x * y }\n",
	})
	// Not committed: a line before Add moves every function down one line.
	writeFiles(t, dir, map[string]string{
		"calc.go": "package calc\n\n// Calc's functions.\nfunc Add(x, y int) int { return x + y }\nfunc Sub(a, b int) int { return a - b }\nfunc Mul(x, y int) int { return x * y }\n",
	})
	result := mutateSince(t, dir, base+"...")
	want := []string{
		`4:35 ArithmeticOperator "-" Killed [TestCalc]`,
		`6:35 ArithmeticOperator "/" Killed [TestCalc]`,
	}
	if got := summarize(result.Report.Files["calc.go"]); !slices.Equal(got, want) {
		t.Errorf("calc.go's bugs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// git diff leaves out a file git does not track yet, so a new file not yet
// added would otherwise get no bugs.
func TestMutate_SinceCountsANewFileGitDoesNotTrackYet(t *testing.T) {
	dir, _ := changedRepo(t, calcFiles, map[string]string{"calc.go": calcFiles["calc.go"] + "\n"})
	writeFiles(t, dir, map[string]string{"half.go": "package calc\n\nfunc Half(a int) int { return a / 2 }\n"})
	result := mutateSince(t, dir, "HEAD")
	if got := summarize(result.Report.Files["half.go"]); len(got) != 1 {
		t.Errorf("half.go's bugs = %v, want its one bug", got)
	}
}

// A moved file is read as one deleted and one added, whatever the user's
// diff.renames says, so every line of it counts as touched.
func TestMutate_SinceAMovedFileIsTouchedThroughout(t *testing.T) {
	dir, _ := changedRepo(t, calcFiles, map[string]string{"calc.go": calcFiles["calc.go"] + "\n"})
	cmd := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@example.com", "mv", "neg.go", "negate.go")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git mv: %v\n%s", err, out)
	}
	result := mutateSince(t, dir, "HEAD")
	if got := len(result.Report.Files["negate.go"].Mutants); got != 3 {
		t.Errorf("negate.go has %d bugs, want all 3", got)
	}
}

// The report lists every source file the change touched, even one with no
// bug on a touched line, and only those.
func TestMutate_SinceListsEachTouchedFileEvenWithNoBug(t *testing.T) {
	dir, base := changedRepo(t, calcFiles, map[string]string{
		"neg.go": "// Negation.\n" + calcFiles["neg.go"],
	})
	result := mutateSince(t, dir, base)
	if files := keysOf(result.Report.Files); !slices.Equal(files, []string{"neg.go"}) {
		t.Errorf("the report's files are %v, want neg.go alone", files)
	}
	if f := result.Report.Files["neg.go"]; len(f.Mutants) != 0 || f.Source == "" || f.Mutants == nil {
		t.Errorf("neg.go = %d bugs, source %q, want none, with its source", len(f.Mutants), f.Source)
	}
	if result.Planted != 0 {
		t.Errorf("planted %d bugs, want none", result.Planted)
	}
}

// The change touches both functions, and double's bugs are still not
// planted, since only the tests call it.
func TestMutate_SinceSkipsAFunctionOnlyTestsCall(t *testing.T) {
	files := map[string]string{
		"calc.go":      "package calc\n\nfunc Add(a, b int) int { return a + b }\nfunc double(a int) int { return a * 2 }\n",
		"calc_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestCalc(t *testing.T) {\n\tif Add(2, 3) != double(2)+1 {\n\t\tt.Error(\"wrong\")\n\t}\n}\n",
	}
	dir, base := changedRepo(t, files, map[string]string{
		"calc.go": "package calc\n\nfunc Add(x, y int) int { return x + y }\nfunc double(x int) int { return x * 2 }\n",
	})
	result := mutateSince(t, dir, base)
	want := []string{`3:35 ArithmeticOperator "-" Killed [TestCalc]`}
	if got := summarize(result.Report.Files["calc.go"]); !slices.Equal(got, want) {
		t.Errorf("calc.go's bugs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
