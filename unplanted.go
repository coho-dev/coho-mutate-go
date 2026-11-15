package main

import (
	"go/ast"
	"go/token"
)

// skipReason is the kind of bug Plant leaves off a node, and why, worded to
// finish "a bug is no longer planted on".
type skipReason struct {
	kind, why string
}

// unplanted holds each node Plant plants no bug on wherever it sits, with the
// kind of bug and why. They are:
//
//   - the ++ or -- in a for loop's last clause, since counting the other way
//     there usually never ends and a hang costs the whole timeout;
//   - a ++ or -- on a counter only compared with zero, since counting the
//     other way gives -1 where it gave 1, and both are non-zero;
//   - a value of a map literal whose every value is true, since that is a
//     set, and a test per member would only copy the list.
func unplanted(file *ast.File) map[ast.Node]skipReason {
	skip := map[ast.Node]skipReason{}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ForStmt:
			if post, ok := n.Post.(*ast.IncDecStmt); ok {
				skip[post] = skipReason{updateOperator, "a for loop's last clause"}
			}
		case *ast.CompositeLit:
			for _, v := range setValues(n) {
				skip[v] = skipReason{booleanLiteral, "a value of a map literal whose every value is true"}
			}
		}
		return true
	})
	// A function literal outside any function declaration, such as a
	// package-level var's, is a function of its own.
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl, *ast.FuncLit:
			for _, s := range zeroCounterSteps(n) {
				skip[s] = skipReason{updateOperator, "a ++ or -- of a counter only compared with zero"}
			}
			return false
		}
		return true
	})
	return skip
}

// setValues is each value of lit when it is a map literal whose every value
// is true, and nothing otherwise.
func setValues(lit *ast.CompositeLit) []*ast.Ident {
	if _, ok := lit.Type.(*ast.MapType); !ok {
		return nil
	}
	var values []*ast.Ident
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			return nil
		}
		v, ok := kv.Value.(*ast.Ident)
		if !ok || v.Name != "true" {
			return nil
		}
		values = append(values, v)
	}
	return values
}

// zeroCounterSteps is each ++ or -- in fn, a function declaration or
// literal, on a counter only compared with zero: a name declared once in fn,
// with the value 0, then only ever stepped one way, and only read by == 0
// or != 0. Plant resolves no names, so a name used any other way in fn, even
// in a second declaration or a parameter that might be another variable, or
// anywhere outside its declaration's scope, keeps its bugs.
func zeroCounterSteps(fn ast.Node) []*ast.IncDecStmt {
	type uses struct {
		decls, other int
		// scope is the span the declaration's name is visible in.
		scope    [2]token.Pos
		at       []token.Pos
		steps    []*ast.IncDecStmt
		up, down bool
	}
	byName := map[string]*uses{}
	use := func(name string) *uses {
		if byName[name] == nil {
			byName[name] = &uses{}
		}
		return byName[name]
	}
	// counted holds each identifier a use already accounts for. Every other
	// one counts against its name.
	counted := map[*ast.Ident]bool{}
	// scopes is the stack of nodes enclosing the one visited that open a
	// scope a declaration inside them is visible in.
	var scopes []ast.Node
	var stack []ast.Node
	declare := func(id *ast.Ident) {
		u := use(id.Name)
		u.decls++
		scope := scopes[len(scopes)-1]
		u.scope = [2]token.Pos{id.End(), scope.End()}
		counted[id] = true
	}
	ast.Inspect(fn, func(n ast.Node) bool {
		if n == nil {
			if top := stack[len(stack)-1]; len(scopes) > 0 && scopes[len(scopes)-1] == top {
				scopes = scopes[:len(scopes)-1]
			}
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		switch n := n.(type) {
		case *ast.BlockStmt, *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt,
			*ast.TypeSwitchStmt, *ast.SelectStmt, *ast.CaseClause, *ast.CommClause:
			scopes = append(scopes, n)
		case *ast.AssignStmt:
			if n.Tok == token.DEFINE && len(n.Lhs) == len(n.Rhs) {
				for i, l := range n.Lhs {
					if id, ok := l.(*ast.Ident); ok && isZero(n.Rhs[i]) {
						declare(id)
					}
				}
			}
		case *ast.ValueSpec:
			for i, id := range n.Names {
				if (len(n.Values) == 0 && n.Type != nil) || (len(n.Values) == len(n.Names) && isZero(n.Values[i])) {
					declare(id)
				}
			}
		case *ast.IncDecStmt:
			if id, ok := ast.Unparen(n.X).(*ast.Ident); ok {
				u := use(id.Name)
				u.steps = append(u.steps, n)
				u.up = u.up || n.Tok == token.INC
				u.down = u.down || n.Tok == token.DEC
				u.at = append(u.at, id.Pos())
				counted[id] = true
			}
		case *ast.BinaryExpr:
			if n.Op == token.EQL || n.Op == token.NEQ {
				for _, pair := range [][2]ast.Expr{{n.X, n.Y}, {n.Y, n.X}} {
					if id, ok := ast.Unparen(pair[0]).(*ast.Ident); ok && isZero(pair[1]) {
						use(id.Name).at = append(use(id.Name).at, id.Pos())
						counted[id] = true
					}
				}
			}
		case *ast.SelectorExpr:
			// A field or package member of that name is not the variable.
			counted[n.Sel] = true
		case *ast.Ident:
			if !counted[n] {
				use(n.Name).other++
			}
		}
		return true
	})
	var steps []*ast.IncDecStmt
	for _, u := range byName {
		if u.decls != 1 || u.other != 0 || u.up == u.down {
			continue
		}
		inScope := true
		for _, at := range u.at {
			inScope = inScope && at >= u.scope[0] && at < u.scope[1]
		}
		if inScope {
			steps = append(steps, u.steps...)
		}
	}
	return steps
}

// isZero is whether e is the literal 0, perhaps in parentheses.
func isZero(e ast.Expr) bool {
	lit, ok := ast.Unparen(e).(*ast.BasicLit)
	return ok && lit.Kind == token.INT && lit.Value == "0"
}
