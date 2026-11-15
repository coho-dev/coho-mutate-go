package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"sort"
	"strings"
)

// Mutant is one planted bug: a span of a source file replaced by other text.
type Mutant struct {
	// Start and End are the byte offsets of the replaced span, End exclusive.
	Start, End int
	// Replacement is the text put in the span's place.
	Replacement string
	// Mutator names the kind of change, in the report's vocabulary, such as
	// EqualityOperator.
	Mutator string
	// Ignored is the reason a marker gave for skipping this bug, or empty.
	Ignored string
}

// ignoreMarker starts a comment telling the tester to skip the kinds of bug
// it names on the line after it:
//
//	//mutate:ignore UpdateOperator, EqualityOperator: why they are skipped
const ignoreMarker = "//mutate:ignore"

// The kinds of bug the tester plants, named as Stryker's report names them.
const (
	equalityOperator   = "EqualityOperator"
	arithmeticOperator = "ArithmeticOperator"
	logicalOperator    = "LogicalOperator"
	updateOperator     = "UpdateOperator"
	assignmentOperator = "AssignmentOperator"
	booleanLiteral     = "BooleanLiteral"
)

// swaps holds, for each operator, what it is replaced by and the kind of
// change that is, named as Stryker's report names it.
var swaps = map[token.Token]struct {
	mutator string
	to      []string
}{
	token.LSS:        {equalityOperator, []string{"<=", ">="}},
	token.LEQ:        {equalityOperator, []string{"<", ">"}},
	token.GTR:        {equalityOperator, []string{">=", "<="}},
	token.GEQ:        {equalityOperator, []string{">", "<"}},
	token.EQL:        {equalityOperator, []string{"!="}},
	token.NEQ:        {equalityOperator, []string{"=="}},
	token.ADD:        {arithmeticOperator, []string{"-"}},
	token.SUB:        {arithmeticOperator, []string{"+"}},
	token.MUL:        {arithmeticOperator, []string{"/"}},
	token.QUO:        {arithmeticOperator, []string{"*"}},
	token.REM:        {arithmeticOperator, []string{"*"}},
	token.LAND:       {logicalOperator, []string{"||"}},
	token.LOR:        {logicalOperator, []string{"&&"}},
	token.INC:        {updateOperator, []string{"--"}},
	token.DEC:        {updateOperator, []string{"++"}},
	token.ADD_ASSIGN: {assignmentOperator, []string{"-="}},
	token.SUB_ASSIGN: {assignmentOperator, []string{"+="}},
	token.MUL_ASSIGN: {assignmentOperator, []string{"/="}},
	token.QUO_ASSIGN: {assignmentOperator, []string{"*="}},
}

// Plant finds every bug it can plant in one Go source file. A generated file
// gets none, and neither does a function only holds, which only test
// files call.
func Plant(src []byte, only testOnly) ([]Mutant, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	if ast.IsGenerated(file) {
		return nil, nil
	}
	offset := func(p token.Pos) int { return fset.Position(p).Offset }

	var mutants []Mutant
	swap := func(pos token.Pos, tok token.Token) {
		s, ok := swaps[tok]
		if !ok {
			return
		}
		start := offset(pos)
		for _, to := range s.to {
			mutants = append(mutants, Mutant{
				Start: start, End: start + len(tok.String()),
				Replacement: to, Mutator: s.mutator,
			})
		}
	}
	skip := unplanted(file)
	ast.Inspect(file, func(n ast.Node) bool {
		if _, ok := skip[n]; ok {
			return true
		}
		switch n := n.(type) {
		case *ast.BinaryExpr:
			if n.Op == token.ADD && (isStringLit(n.X) || isStringLit(n.Y)) {
				return true
			}
			swap(n.OpPos, n.Op)
		case *ast.AssignStmt:
			swap(n.TokPos, n.Tok)
		case *ast.IncDecStmt:
			swap(n.TokPos, n.Tok)
		case *ast.UnaryExpr:
			if n.Op == token.NOT {
				start := offset(n.OpPos)
				mutants = append(mutants, Mutant{
					Start: start, End: start + 1,
					Mutator: booleanLiteral,
				})
			}
		case *ast.Ident:
			to := map[string]string{"true": "false", "false": "true"}[n.Name]
			if to != "" {
				start := offset(n.Pos())
				mutants = append(mutants, Mutant{
					Start: start, End: start + len(n.Name),
					Replacement: to, Mutator: booleanLiteral,
				})
			}
		}
		return true
	})
	sort.SliceStable(mutants, func(i, j int) bool { return mutants[i].Start < mutants[j].Start })
	skipped := map[lineKind]string{}
	for n, r := range skip {
		skipped[lineKind{fset.Position(n.Pos()).Line, r.kind}] = r.why
	}
	mutants = dropTestOnly(fset, file, mutants, only, skipped)
	if err := applyMarkers(fset, file, mutants, skipped); err != nil {
		return nil, err
	}
	return mutants, nil
}

// dropTestOnly is mutants without those inside a function only holds.
// Each one it drops is noted in skipped, so a marker that skipped it is told
// why it has gone stale.
func dropTestOnly(fset *token.FileSet, file *ast.File, mutants []Mutant, only testOnly, skipped map[lineKind]string) []Mutant {
	tf := fset.File(file.Package)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || !only[declName(fn)] {
			continue
		}
		start, end := tf.Offset(fn.Pos()), tf.Offset(fn.End())
		mutants = slices.DeleteFunc(mutants, func(m Mutant) bool {
			if m.Start < start || m.Start >= end {
				return false
			}
			at := lineKind{tf.Line(tf.Pos(m.Start)), m.Mutator}
			if _, ok := skipped[at]; !ok {
				skipped[at] = "a line of a function only test files call"
			}
			return true
		})
	}
	return mutants
}

// applyMarkers sets Ignored on each mutant a marker skips. A marker it cannot
// read, or one naming a kind it skips none of, is refused: the first would
// hide a bug nobody chose to hide, and the second has gone stale. unplanted
// holds why Plant planted no bug of a kind on a line, so a marker that once
// skipped one is told why it has gone stale.
func applyMarkers(fset *token.FileSet, file *ast.File, mutants []Mutant, unplanted map[lineKind]string) error {
	tf := fset.File(file.Package)
	for _, group := range file.Comments {
		for _, c := range group.List {
			rest, ok := strings.CutPrefix(c.Text, ignoreMarker)
			if !ok {
				continue
			}
			line := tf.Line(c.Pos())
			bad := func(why string) error {
				return fmt.Errorf("line %d: %s: %s, as in %s UpdateOperator: why", line, c.Text, why, ignoreMarker)
			}
			kindList, reason, ok := strings.Cut(rest, ":")
			reason = strings.TrimSpace(reason)
			if !ok || reason == "" {
				return bad("it gives no reason")
			}
			kinds := map[string]bool{}
			for _, k := range strings.Split(kindList, ",") {
				k = strings.TrimSpace(k)
				if k == "" {
					continue
				}
				if !isPlantedKind(k) {
					return bad(fmt.Sprintf("%s is no kind of bug the tester plants", k))
				}
				kinds[k] = true
			}
			if len(kinds) == 0 {
				return bad("it names no kind of bug")
			}
			skipped := map[string]bool{}
			for i, m := range mutants {
				if kinds[m.Mutator] && tf.Line(tf.Pos(m.Start)) == line+1 {
					mutants[i].Ignored = reason
					skipped[m.Mutator] = true
				}
			}
			// Each kind must skip something, or it would hide whatever bug of
			// that kind a later edit puts on the line, for a reason nobody
			// gave it.
			for k := range kinds {
				if why, ok := unplanted[lineKind{line + 1, k}]; ok && !skipped[k] {
					return fmt.Errorf("line %d: %s: line %d has no %s bug to skip: a bug is no longer planted on %s, so delete this marker", line, c.Text, line+1, k, why)
				}
				if !skipped[k] {
					return bad(fmt.Sprintf("line %d has no %s bug to skip", line+1, k))
				}
			}
		}
	}
	return nil
}

// lineKind is a line of a file and a kind of bug.
type lineKind struct {
	line int
	kind string
}

// isPlantedKind is whether the tester plants bugs of kind k: those in swaps, and
// the BooleanLiteral bugs Plant makes itself.
func isPlantedKind(k string) bool {
	if k == booleanLiteral {
		return true
	}
	for _, sw := range swaps {
		if sw.mutator == k {
			return true
		}
	}
	return false
}

// isStringLit is whether e is a string literal, perhaps in parentheses.
func isStringLit(e ast.Expr) bool {
	lit, ok := ast.Unparen(e).(*ast.BasicLit)
	return ok && lit.Kind == token.STRING
}

// Apply returns src with the mutant planted in it, leaving src as it was.
func (m Mutant) Apply(src []byte) []byte {
	out := make([]byte, 0, len(src)+len(m.Replacement))
	out = append(out, src[:m.Start]...)
	out = append(out, m.Replacement...)
	return append(out, src[m.End:]...)
}
