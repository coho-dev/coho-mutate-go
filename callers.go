package main

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// funcName names a function declared in a file: the name of its receiver's
// type, empty for a plain function, and its own name.
type funcName struct{ recv, name string }

func declName(fn *ast.FuncDecl) funcName {
	name := funcName{name: fn.Name.Name}
	if fn.Recv != nil && len(fn.Recv.List) == 1 {
		t := fn.Recv.List[0].Type
		for {
			switch e := t.(type) {
			case *ast.StarExpr:
				t = e.X
				continue
			case *ast.ParenExpr:
				t = e.X
				continue
			case *ast.IndexExpr:
				t = e.X
				continue
			case *ast.IndexListExpr:
				t = e.X
				continue
			case *ast.Ident:
				name.recv = e.Name
			}
			break
		}
	}
	return name
}

// testOnly holds the functions of a package that only test files name. A
// funcName picks out one declaration in a package's build, so the set holds
// for every file of it.
type testOnly map[funcName]bool

// candidateKind is a kind of function that can be test-only. Each kind says
// which uses of its name count as naming it.
type candidateKind int

const (
	// unexportedFunc is an unexported plain function, which only its own
	// package names, and never after a dot.
	unexportedFunc candidateKind = iota
	// unexportedMethod is an unexported method, which only its own package
	// selects after a dot. An interface of the package that lists its name
	// keeps its bugs, since a call through the interface names no type.
	unexportedMethod
	// internalFunc is an exported plain function of an internal package,
	// which any package of the module that imports it can name, after a dot
	// or alone through a dot import.
	internalFunc
)

// names is whether a use of the function's name counts: selected says it
// followed a dot, and samePkg that its file is in the function's package.
func (k candidateKind) names(selected, samePkg bool) bool {
	switch k {
	case unexportedFunc:
		return samePkg && !selected
	case unexportedMethod:
		return samePkg && selected
	default:
		return true
	}
}

// otherPackagesName is whether another package can name a function of the
// kind, so every package that imports its own has to be read too.
func (k candidateKind) otherPackagesName() bool { return k == internalFunc }

// interfacesCall is whether an interface listing the function's name could
// call it, which names no type, so such an interface keeps its bugs.
func (k candidateKind) interfacesCall() bool { return k == unexportedMethod }

// filePlace is where a file findTestOnly reads sits.
type filePlace int

const (
	// placeSource is a source file of the package under test, the only
	// place its functions are declared.
	placeSource filePlace = iota
	// placeInPackageTest is a test file of the package under test, in its own
	// package.
	placeInPackageTest
	// placeIgnored is a file of the package under test that this machine's
	// build leaves out. Its package clause says whether it is in the
	// package.
	placeIgnored
	// placeElsewhere is a file in another package: an external test package, or
	// another package of the module.
	placeElsewhere
)

// goFile is a file findTestOnly reads names in.
type goFile struct {
	path  string
	place filePlace
}

// test is whether f is a test file, which go list names by the _test.go
// suffix alone, whether or not the build takes it.
func (f goFile) test() bool { return strings.HasSuffix(f.path, "_test.go") }

// fileLists is each list of Go files go list gives for p, with the place
// a file in it has when p is the package under test. Every file of p is in
// one of them, including those this machine's build leaves out.
func fileLists(p goPackage) []struct {
	names []string
	place filePlace
} {
	return []struct {
		names []string
		place filePlace
	}{
		{p.GoFiles, placeSource},
		{p.CgoFiles, placeSource},
		{p.TestGoFiles, placeInPackageTest},
		{p.XTestGoFiles, placeElsewhere},
		{p.IgnoredGoFiles, placeIgnored},
	}
}

// packageFiles is every Go file of the package under test.
func packageFiles(p goPackage) []goFile {
	var files []goFile
	for _, list := range fileLists(p) {
		for _, name := range list.names {
			files = append(files, goFile{path: filepath.Join(p.Dir, name), place: list.place})
		}
	}
	return files
}

// otherFiles is every Go file of p, another package than the one under
// test.
func otherFiles(p goPackage) []goFile {
	var files []goFile
	for _, list := range fileLists(p) {
		for _, name := range list.names {
			files = append(files, goFile{path: filepath.Join(p.Dir, name), place: placeElsewhere})
		}
	}
	return files
}

// findTestOnly finds the functions of pkg whose only callers are test files,
// in pkg or in any other package of its module. Only a function nothing
// outside the module can name counts: an unexported one, or one in an
// internal package whose parent folder is inside the module, with no
// module nested under that folder that may import it. A function counts as
// named wherever its name appears in a file that is not a test, so
// a name it shares with anything else keeps its bugs.
//
// It reads names, not types, and wherever it cannot tell it keeps a
// function's bugs: an exported method, which an interface outside the module
// can call, and a function a //go:linkname or //export comment in its own
// file names. A //go:linkname in another package that pulls the function in
// by its full name is not looked for. A source file the build leaves out on
// this machine counts as a file that is not a test. A file that does not
// parse, or a package of the module go list cannot read, keeps the bugs of
// every function it could have named.
func findTestOnly(ctx context.Context, pkg goPackage) (testOnly, error) {
	fset := token.NewFileSet()
	candidates := map[funcName]candidateKind{}
	for _, name := range pkg.GoFiles {
		file, err := parser.ParseFile(fset, filepath.Join(pkg.Dir, name), nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		named := directed(file)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			n := declName(fn)
			if n.name == "init" || n.name == "_" || (n.name == "main" && pkg.Name == "main" && n.recv == "") || named[n.name] {
				continue
			}
			switch {
			case !ast.IsExported(n.name) && n.recv == "":
				candidates[n] = unexportedFunc
			case !ast.IsExported(n.name):
				candidates[n] = unexportedMethod
			case n.recv == "" && isInternal(pkg):
				candidates[n] = internalFunc
			}
		}
	}
	// dropShared keeps the bugs of every function another package can
	// name, when one of them cannot be read.
	dropShared := func() {
		for n, k := range candidates {
			if k.otherPackagesName() {
				delete(candidates, n)
			}
		}
	}

	files := packageFiles(pkg)
	if slices.ContainsFunc(slices.Collect(maps.Values(candidates)), candidateKind.otherPackagesName) {
		importers, ok, err := importersOf(ctx, pkg)
		if err != nil {
			return nil, err
		}
		if !ok {
			dropShared()
		}
		for _, p := range importers {
			files = append(files, otherFiles(p)...)
		}
	}

	byName := map[string][]funcName{}
	for n := range candidates {
		byName[n.name] = append(byName[n.name], n)
	}
	inTest, inOther := map[funcName]bool{}, map[funcName]bool{}
	listed := map[string]bool{}
	for _, f := range files {
		file, err := parser.ParseFile(fset, f.path, nil, parser.SkipObjectResolution)
		switch {
		case err == nil:
		case f.place == placeIgnored:
			// It could name any function of the package.
			return testOnly{}, nil
		case f.place == placeElsewhere:
			// It could name any function of the package another package
			// can name. Its own package's tests are not run, so the run
			// goes on.
			dropShared()
			continue
		default:
			// The package's own tests do not build, so the run would stop
			// anyway.
			return nil, err
		}
		samePkg := f.place == placeSource || f.place == placeInPackageTest || (f.place == placeIgnored && file.Name.Name == pkg.Name)
		for _, decl := range file.Decls {
			var self funcName
			if fn, ok := decl.(*ast.FuncDecl); ok && f.place == placeSource {
				self = declName(fn)
			}
			names(decl, func(name string, selected bool) {
				for _, n := range byName[name] {
					if n == self || !candidates[n].names(selected, samePkg) {
						continue
					}
					if f.test() {
						inTest[n] = true
					} else {
						inOther[n] = true
					}
				}
			}, func(method string) {
				if samePkg {
					listed[method] = true
				}
			})
		}
	}

	only := testOnly{}
	for n, k := range candidates {
		if inTest[n] && !inOther[n] && !(k.interfacesCall() && listed[n.name]) {
			only[n] = true
		}
	}
	return only, nil
}

// importersOf is every other package of pkg's module that imports it, from
// any of its files, including those this machine's build leaves out. ok is
// false when it cannot tell: pkg is in no module, go list cannot read one of
// the module's packages, or a file the build leaves out does not parse.
func importersOf(ctx context.Context, pkg goPackage) (importers []goPackage, ok bool, err error) {
	if pkg.Module.Path == "" {
		return nil, false, nil
	}
	all, err := goList(ctx, pkg.Dir, pkg.Module.Path+"/...", "-e")
	if err != nil {
		return nil, false, err
	}
	listed := map[string]bool{}
	for _, p := range all {
		listed[filepath.Clean(p.Dir)] = true
	}
	if unseenImporter(ctx, pkg, listed) {
		return nil, false, nil
	}
	fset := token.NewFileSet()
	for _, p := range all {
		if p.Error != nil {
			return nil, false, nil
		}
		if p.ImportPath == pkg.ImportPath {
			continue
		}
		imports := slices.Concat(p.Imports, p.TestImports, p.XTestImports)
		for _, name := range p.IgnoredGoFiles {
			file, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, parser.ImportsOnly)
			if err != nil {
				return nil, false, nil
			}
			for _, spec := range file.Imports {
				imports = append(imports, strings.Trim(spec.Path.Value, "`\""))
			}
		}
		if slices.Contains(imports, pkg.ImportPath) {
			importers = append(importers, p)
		}
	}
	return importers, true, nil
}

// internalParent is the path, after the module path, of the folder whose
// packages alone may import pkg, by Go's rule for a path element named
// internal: the parent of the last such element. ok is false when pkg is in
// no internal package below its module's root. An internal element in the
// module path itself counts for nothing, since any module under its parent
// may import the package.
func internalParent(pkg goPackage) (parent []string, ok bool) {
	rest, ok := strings.CutPrefix(pkg.ImportPath, pkg.Module.Path+"/")
	if !ok || pkg.Module.Path == "" {
		return nil, false
	}
	elems := strings.Split(rest, "/")
	for i := len(elems) - 1; i >= 0; i-- {
		if elems[i] == "internal" {
			return elems[:i], true
		}
	}
	return nil, false
}

// isInternal is whether pkg is in an internal package whose parent folder is
// inside its module.
func isInternal(pkg goPackage) bool {
	_, ok := internalParent(pkg)
	return ok
}

// unseenImporter is whether code go list did not name may import pkg, or
// might. It looks through the folder whose packages alone may import pkg,
// and finds three things there:
//
//   - a module nested inside pkg's module whose module path is under that
//     folder's import path, as a v2 major version's is. Go lets it import
//     pkg, and go list's pattern for pkg's module leaves it out;
//   - a Go file in a folder that is no package listed, one whose every file
//     this machine's build leaves out, such as a Windows-only command on
//     Linux. Go leaves out a testdata or vendor folder, or one whose name
//     starts with a dot or an underscore, so it does too, modules nested
//     there included;
//   - a folder or go.mod it cannot read, which could hold either.
func unseenImporter(ctx context.Context, pkg goPackage, listed map[string]bool) bool {
	parent, _ := internalParent(pkg)
	root := filepath.Join(append([]string{pkg.Module.Dir}, parent...)...)
	importer := strings.Join(append([]string{pkg.Module.Path}, parent...), "/")
	found := false
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			found = true
			return fs.SkipAll
		}
		if !d.IsDir() {
			if strings.HasSuffix(path, ".go") && !listed[filepath.Dir(path)] {
				found = true
				return fs.SkipAll
			}
			return nil
		}
		if path != filepath.Clean(pkg.Module.Dir) {
			mod := filepath.Join(path, "go.mod")
			if _, err := os.Stat(mod); err == nil {
				// A go.mod go cannot read could declare any path. One of
				// another path holds packages that cannot import pkg.
				modPath, err := modulePath(ctx, mod)
				if err != nil || modPath == importer || strings.HasPrefix(modPath, importer+"/") {
					found = true
					return fs.SkipAll
				}
				return fs.SkipDir
			}
		}
		if name := d.Name(); path != root && (name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
			return fs.SkipDir
		}
		return nil
	})
	return found
}

// modulePath is the module path the go.mod file at path declares, as go
// mod edit reads it.
func modulePath(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, "go", "mod", "edit", "-json", path).Output()
	if err != nil {
		return "", fmt.Errorf("read the module path of %s: %w", path, err)
	}
	var mod struct{ Module struct{ Path string } }
	if err := json.Unmarshal(out, &mod); err != nil {
		return "", fmt.Errorf("read go mod edit's answer for %s: %w", path, err)
	}
	return mod.Module.Path, nil
}

// directed is each name a //go:linkname or //export comment in file names,
// which code the tool cannot see may call.
func directed(file *ast.File) map[string]bool {
	named := map[string]bool{}
	for _, group := range file.Comments {
		for _, c := range group.List {
			fields := strings.Fields(c.Text)
			if len(fields) >= 2 && (fields[0] == "//go:linkname" || fields[0] == "//export") {
				named[fields[1]] = true
			}
		}
	}
	return named
}

// names calls use with each identifier in node, saying whether it follows a
// dot, and listed with each method name an interface type in node lists. A
// function or method declaration's own name is no use of anything, so it is
// left out.
func names(node ast.Node, use func(name string, selected bool), listed func(method string)) {
	var visit func(ast.Node) bool
	visit = func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			if n.Recv != nil {
				ast.Inspect(n.Recv, visit)
			}
			ast.Inspect(n.Type, visit)
			if n.Body != nil {
				ast.Inspect(n.Body, visit)
			}
			return false
		case *ast.SelectorExpr:
			ast.Inspect(n.X, visit)
			use(n.Sel.Name, true)
			return false
		case *ast.InterfaceType:
			for _, field := range n.Methods.List {
				for _, name := range field.Names {
					listed(name.Name)
				}
			}
		case *ast.Ident:
			use(n.Name, false)
		}
		return true
	}
	ast.Inspect(node, visit)
}
