package main

import (
	"bytes"
	"strings"
	"testing"
)

// A profile go test wrote for a package with this q.go, from a run that
// called Max(1, 2) and never called Unused:
//
//	3 func Max(a, b int) int {
//	4 	if a > b {
//	5 		return a
//	6 	}
//	7 	return b
//	8 }
//	9
//	10 func Unused(x int) bool { return x > 0 }
var qSource = []byte("package probe\n\nfunc Max(a, b int) int {\n\tif a > b {\n\t\treturn a\n\t}\n\treturn b\n}\n\nfunc Unused(x int) bool { return x > 0 }\n")

// offsetOf is the byte offset of a line and a column counted in bytes, both
// from one, as the profile counts them.
func offsetOf(src []byte, line, col int) int {
	offset := 0
	for l := 1; l < line; l++ {
		offset += bytes.IndexByte(src[offset:], '\n') + 1
	}
	return offset + col - 1
}

const profile = `mode: set
example.com/probe/q.go:3.24,4.11 1 1
example.com/probe/q.go:4.11,6.3 1 0
example.com/probe/q.go:7.2,7.10 1 1
example.com/probe/q.go:10.27,10.41 1 0
`

func TestCoverage_Unreached(t *testing.T) {
	cov, err := readCoverage(strings.NewReader(profile))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		file      string
		line, col int
		want      bool
	}{
		{"the condition a test ran", "q.go", 4, 7, false},
		{"the branch no test took", "q.go", 5, 3, true},
		{"the function no test called", "q.go", 10, 36, true},
		{"a line in no block", "q.go", 9, 1, false},
		{"a line before every function", "q.go", 2, 1, false},
		{"a file the profile does not name", "r.go", 4, 7, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := unreachedAt(t, cov, "/somewhere/"+c.file, qSource, c.line, c.col); got != c.want {
				t.Errorf("unreached(%s:%d.%d) = %v, want %v", c.file, c.line, c.col, got, c.want)
			}
		})
	}
}

// Blocks that meet share a position: the end of one is the start of the
// next. A position any block holding it reached is reached.
func TestCoverage_APositionTwoBlocksShareIsReachedIfEitherIs(t *testing.T) {
	cov, err := readCoverage(strings.NewReader(profile))
	if err != nil {
		t.Fatal(err)
	}
	if unreachedAt(t, cov, "/somewhere/q.go", qSource, 4, 11) {
		t.Error("4.11 ends a block that ran, but read as unreached")
	}
}

// unreachedAt is whether cov reads the line and column of the file, whose
// source is src, as unreached.
func unreachedAt(t *testing.T, cov coverage, file string, src []byte, line, col int) bool {
	t.Helper()
	fc, err := cov.file(file, src)
	if err != nil {
		t.Fatal(err)
	}
	return fc.unreached(offsetOf(src, line, col))
}

// A profile go test wrote for a package with this case.go, from a run that
// called Kind(5) and never called Pick. The profile has no block for a case
// expression: the one before each statement ends at its {, and each case
// body's starts after its colon.
//
//	3 func Kind(n int) string {
//	4 	switch {
//	5 	case n < 0:
//	6 		return "neg"
//	7 	case n > 9:
//	8 		return "big"
//	9 	}
//	10 	return "small"
//	11 }
//	12
//	13 func Pick(c chan int, n int) int {
//	14 	select {
//	15 	case c <- n + 1:
//	16 		return 0
//	17 	default:
//	18 		return 1
//	19 	}
//	20 }
var caseSource = []byte("package probe\n\nfunc Kind(n int) string {\n\tswitch {\n\tcase n < 0:\n\t\treturn \"neg\"\n\tcase n > 9:\n\t\treturn \"big\"\n\t}\n\treturn \"small\"\n}\n\nfunc Pick(c chan int, n int) int {\n\tselect {\n\tcase c <- n + 1:\n\t\treturn 0\n\tdefault:\n\t\treturn 1\n\t}\n}\n")

const caseProfile = `mode: set
example.com/probe/case.go:4.2,4.9 1 1
example.com/probe/case.go:6.3,6.15 1 0
example.com/probe/case.go:8.3,8.15 1 0
example.com/probe/case.go:10.2,10.16 1 1
example.com/probe/case.go:14.2,14.9 1 0
example.com/probe/case.go:16.3,16.11 1 0
example.com/probe/case.go:18.3,18.11 1 0
`

// A case expression reads as reached exactly when its statement's start did.
func TestCoverage_ACaseExpressionReadsAsItsStatementDoes(t *testing.T) {
	cov, err := readCoverage(strings.NewReader(caseProfile))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		line, col int
		want      bool
	}{
		{"a switch case in a function a test called", 5, 9, false},
		{"a later switch case in a function a test called", 7, 9, false},
		{"a select case in a function no test called", 15, 14, true},
		{"the gap between functions", 12, 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := unreachedAt(t, cov, "/somewhere/case.go", caseSource, c.line, c.col); got != c.want {
				t.Errorf("unreached(case.go:%d.%d) = %v, want %v", c.line, c.col, got, c.want)
			}
		})
	}
}

// A case expression that holds a function literal reads the literal's body
// by the literal's own blocks.
func TestCoverage_AFunctionLiteralInACaseKeepsItsOwnBlocks(t *testing.T) {
	src := []byte("package probe\n\nfunc F(n int) int {\n\tswitch {\n\tcase func() bool { return n > 0 }():\n\t\treturn 1\n\t}\n\treturn 0\n}\n")
	// F ran, but the literal is never called in this made-up profile.
	profile := "mode: set\nexample.com/probe/f.go:3.20,4.9 1 1\nexample.com/probe/f.go:5.20,5.34 1 0\nexample.com/probe/f.go:6.3,6.11 1 0\nexample.com/probe/f.go:8.2,8.10 1 1\n"
	cov, err := readCoverage(strings.NewReader(profile))
	if err != nil {
		t.Fatal(err)
	}
	if !unreachedAt(t, cov, "/somewhere/f.go", src, 5, 30) {
		t.Error("the literal's body no test ran read as reached, from its switch")
	}
	if unreachedAt(t, cov, "/somewhere/f.go", src, 5, 7) {
		t.Error("the case expression outside the literal read as unreached, though its switch ran")
	}
}

func TestReadCoverage_RefusesALineItCannotRead(t *testing.T) {
	for _, bad := range []string{
		"mode: set\nq.go:3.24 1 1\n",
		"mode: set\nq.go:3.24,4.11 1\n",
		"mode: set\nq.go:a.24,4.11 1 1\n",
	} {
		if _, err := readCoverage(strings.NewReader(bad)); err == nil {
			t.Errorf("readCoverage accepted %q", bad)
		}
	}
}
