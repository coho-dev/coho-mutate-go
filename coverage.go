package main

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// coverage is which parts of a package's files any test ran, read from a
// `go test -coverprofile` profile.
type coverage struct {
	// blocks is keyed by the file's base name, which is unique in a package.
	blocks map[string][]coverBlock
}

type coverBlock struct {
	startLine, startCol, endLine, endCol int
	hit                                  bool
}

func (b coverBlock) holds(line, col int) bool {
	after := line > b.startLine || (line == b.startLine && col >= b.startCol)
	before := line < b.endLine || (line == b.endLine && col <= b.endCol)
	return after && before
}

// readCoverage reads a profile written with -covermode=set.
func readCoverage(r io.Reader) (coverage, error) {
	cov := coverage{blocks: map[string][]coverBlock{}}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		file, block, err := readCoverLine(line)
		if err != nil {
			return coverage{}, err
		}
		cov.blocks[file] = append(cov.blocks[file], block)
	}
	if err := scanner.Err(); err != nil {
		return coverage{}, fmt.Errorf("read the coverage profile: %w", err)
	}
	return cov, nil
}

// readCoverLine reads one line, file.go:startLine.startCol,endLine.endCol
// statements count.
func readCoverLine(line string) (string, coverBlock, error) {
	bad := func() (string, coverBlock, error) {
		return "", coverBlock{}, fmt.Errorf("read the coverage profile: cannot read %q", line)
	}
	colon := strings.LastIndex(line, ":")
	if colon < 0 {
		return bad()
	}
	fields := strings.Fields(line[colon+1:])
	if len(fields) != 3 {
		return bad()
	}
	from, to, ok := strings.Cut(fields[0], ",")
	if !ok {
		return bad()
	}
	var b coverBlock
	var err error
	if b.startLine, b.startCol, err = readLineCol(from); err != nil {
		return bad()
	}
	if b.endLine, b.endCol, err = readLineCol(to); err != nil {
		return bad()
	}
	count, err := strconv.Atoi(fields[2])
	if err != nil {
		return bad()
	}
	b.hit = count > 0
	return path.Base(line[:colon]), b, nil
}

func readLineCol(s string) (int, int, error) {
	l, c, ok := strings.Cut(s, ".")
	if !ok {
		return 0, 0, fmt.Errorf("no column in %q", s)
	}
	line, err := strconv.Atoi(l)
	if err != nil {
		return 0, 0, err
	}
	col, err := strconv.Atoi(c)
	return line, col, err
}

// fileCoverage is which parts of one file any test ran. It knows where the
// file's case clauses sit, since Go's profile gives a case expression no
// block of its own.
type fileCoverage struct {
	blocks []coverBlock
	src    []byte
	cases  []caseSpan
}

// caseSpan is the byte span of one case clause's expressions, End
// exclusive, and the offset of the statement whose coverage stands for it.
type caseSpan struct {
	start, end, stmt int
}

// file is the coverage of the file at path, whose source is src.
func (c coverage) file(path string, src []byte) (fileCoverage, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.SkipObjectResolution)
	if err != nil {
		return fileCoverage{}, fmt.Errorf("read the coverage of %s: %w", path, err)
	}
	offset := func(p token.Pos) int { return fset.Position(p).Offset }
	fc := fileCoverage{blocks: c.blocks[filepath.Base(path)], src: src}
	ast.Inspect(f, func(n ast.Node) bool {
		var body *ast.BlockStmt
		switch n := n.(type) {
		case *ast.SwitchStmt:
			body = n.Body
		case *ast.TypeSwitchStmt:
			body = n.Body
		case *ast.SelectStmt:
			body = n.Body
		default:
			return true
		}
		stmt := offset(n.Pos())
		for _, clause := range body.List {
			switch cl := clause.(type) {
			case *ast.CaseClause:
				if len(cl.List) > 0 {
					fc.cases = append(fc.cases, caseSpan{offset(cl.List[0].Pos()), offset(cl.List[len(cl.List)-1].End()), stmt})
				}
			case *ast.CommClause:
				if cl.Comm != nil {
					fc.cases = append(fc.cases, caseSpan{offset(cl.Comm.Pos()), offset(cl.Comm.End()), stmt})
				}
			}
		}
		return true
	})
	return fc, nil
}

// unreached says whether no test ran the code at the byte offset. A position
// in a case expression reads as its statement's start does. Any other
// position in no block at all, such as a package-level declaration, is not
// known to be unreached, so it reads as reached.
func (f fileCoverage) unreached(offset int) bool {
	lineStart := bytes.LastIndexByte(f.src[:offset], '\n') + 1
	line := bytes.Count(f.src[:offset], []byte("\n")) + 1
	// The profile counts columns in bytes, from one.
	col := offset - lineStart + 1
	held := false
	for _, b := range f.blocks {
		if b.holds(line, col) {
			if b.hit {
				return false
			}
			held = true
		}
	}
	if held {
		return true
	}
	for _, c := range f.cases {
		if offset >= c.start && offset < c.end {
			return f.unreached(c.stmt)
		}
	}
	return false
}
