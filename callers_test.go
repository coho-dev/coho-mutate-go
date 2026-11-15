package main

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// testOnlyIn writes files into a module example.com/m and answers what
// findTestOnly finds in the package at dir, as function names, a method after its type.
func testOnlyIn(t *testing.T, files map[string]string, dir string) []string {
	t.Helper()
	return testOnlyInModule(t, "example.com/m", files, dir)
}

func testOnlyInModule(t *testing.T, module string, files map[string]string, dir string) []string {
	t.Helper()
	return testOnlyInModuleAfter(t, module, files, dir, func(string) {})
}

// testOnlyInAfter is testOnlyIn, calling prepare with the module's folder
// once the files are written.
func testOnlyInAfter(t *testing.T, files map[string]string, dir string, prepare func(root string)) []string {
	t.Helper()
	return testOnlyInModuleAfter(t, "example.com/m", files, dir, prepare)
}

func testOnlyInModuleAfter(t *testing.T, module string, files map[string]string, dir string, prepare func(root string)) []string {
	t.Helper()
	t.Setenv("GOWORK", "off")
	root := t.TempDir()
	files["go.mod"] = "module " + module + "\n\ngo 1.27.0\n"
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prepare(root)
	ctx := context.Background()
	pkg, err := listPackage(ctx, filepath.Join(root, dir))
	if err != nil {
		t.Fatal(err)
	}
	only, err := findTestOnly(ctx, pkg)
	if err != nil {
		t.Fatalf("findTestOnly: %v", err)
	}
	var got []string
	for fn := range only {
		name := fn.name
		if fn.recv != "" {
			name = fn.recv + "." + name
		}
		got = append(got, name)
	}
	slices.Sort(got)
	return got
}

var internalModule = map[string]string{
	"internal/fix/fix.go": `package fix

import _ "unsafe"

var hooks = []func() int{passed}

type T struct{}

type stepper interface{ step() }

func onlyTests() int { return 1 }

func recursive(n int) int {
	if n == 0 {
		return 0
	}
	return recursive(n - 1)
}

func calledHere() int { return 1 }

func passed() int { return 2 }

func unused() int { return 3 }

func (T) method() int { return 4 }

func (*T) step() {}

func (T) Exported() int { return 5 }

func Run() int { return calledHere() }

func Build() int { return 6 }

func Shared() int { return 7 }

func elsewhere() int { return 8 }

//go:linkname linked
func linked() int { return 9 }

func init() {}
`,
	"internal/fix/fix_never.go": "//go:build never\n\npackage fix\n\nvar _ = elsewhere\n",
	"internal/fix/fix_test.go": `package fix

import "testing"

func TestFix(t *testing.T) {
	var x T
	_ = onlyTests() + recursive(1) + calledHere() + passed() + x.method() + x.Exported() + elsewhere() + linked()
	x.step()
}
`,
	"user/user.go": `package user

import "example.com/m/internal/fix"

func U() int { return fix.Shared() + fix.Run() }
`,
	"user/user_test.go": `package user

import (
	"testing"

	"example.com/m/internal/fix"
)

func TestU(t *testing.T) { _ = fix.Build() + fix.Shared() }
`,
	"pub/pub.go": "package pub\n\nfunc Fixture() int { return 1 }\n\nfunc local() int { return 2 }\n",
	"pub/pub_test.go": `package pub

import "testing"

func TestPub(t *testing.T) { _ = Fixture() + local() }
`,
}

// Of an internal package's functions, those only test files name, here or
// in another package of the module, are test-only. Any name a file that is
// not a test uses keeps a function's bugs, and so does an exported method,
// a method an interface lists, and a function nothing calls.
func TestFindTestOnly_InAnInternalPackage(t *testing.T) {
	got := testOnlyIn(t, maps.Clone(internalModule), "internal/fix")
	want := []string{"Build", "T.method", "onlyTests", "recursive"}
	if !slices.Equal(got, want) {
		t.Errorf("test-only functions = %q, want %q", got, want)
	}
}

// Outside an internal package, an exported function may have callers in
// another module, so only unexported ones can be test-only.
func TestFindTestOnly_OutsideAnInternalPackage(t *testing.T) {
	got := testOnlyIn(t, maps.Clone(internalModule), "pub")
	if want := []string{"local"}; !slices.Equal(got, want) {
		t.Errorf("test-only functions = %q, want %q", got, want)
	}
}

// A package of the module go list cannot read might call any exported
// function of the internal package.
func TestFindTestOnly_APackageGoListCannotReadKeepsExportedFunctions(t *testing.T) {
	files := maps.Clone(internalModule)
	files["broken/broken.go"] = "package broken\n\nimport \"example.com/m/nowhere\"\n"
	files["broken/other.go"] = "package other\n"
	got := testOnlyIn(t, files, "internal/fix")
	want := []string{"T.method", "onlyTests", "recursive"}
	if !slices.Equal(got, want) {
		t.Errorf("test-only functions = %q, want %q", got, want)
	}
}

// A file of another package that does not parse could name any exported
// function, so those keep their bugs, and the run goes on.
func TestFindTestOnly_AnImporterThatDoesNotParseKeepsExportedFunctions(t *testing.T) {
	files := maps.Clone(internalModule)
	files["user/broken.go"] = "package user\n\nimport \"example.com/m/internal/fix\"\n\nfunc B() int { return fix.Build( }\n"
	got := testOnlyIn(t, files, "internal/fix")
	want := []string{"T.method", "onlyTests", "recursive"}
	if !slices.Equal(got, want) {
		t.Errorf("test-only functions = %q, want %q", got, want)
	}
}

// An internal element in the module's own path lets any module under its
// parent import the package, so it counts for nothing.
func TestFindTestOnly_AnInternalModulePathIsNotInternal(t *testing.T) {
	files := map[string]string{
		"fix/fix.go":      "package fix\n\nfunc Positive(n int) bool { return n > 0 }\n",
		"fix/fix_test.go": "package fix\n\nimport \"testing\"\n\nfunc TestP(t *testing.T) { _ = Positive(1) }\n",
	}
	if got := testOnlyInModule(t, "example.com/internal/b", files, "fix"); len(got) != 0 {
		t.Errorf("test-only functions = %q, want none: other modules can import example.com/internal/b/fix", got)
	}
}

// A nested module under the internal element's parent folder, such as a
// v2 major version, can import the package too, and the tool doesn't read
// it, so the package's exported functions keep their bugs.
func TestFindTestOnly_ANestedModuleKeepsExportedFunctions(t *testing.T) {
	files := maps.Clone(internalModule)
	files["v2/go.mod"] = "module example.com/m/v2\n\ngo 1.27.0\n"
	got := testOnlyIn(t, files, "internal/fix")
	want := []string{"T.method", "onlyTests", "recursive"}
	if !slices.Equal(got, want) {
		t.Errorf("test-only functions = %q, want %q", got, want)
	}
}

// A nested module whose path is not under the internal element's parent,
// such as a test fixture, cannot import the package, so it changes nothing.
func TestFindTestOnly_ANestedModuleOfAnotherPathChangesNothing(t *testing.T) {
	files := maps.Clone(internalModule)
	files["testdata/other/go.mod"] = "module example.com/other\n\ngo 1.27.0\n"
	got := testOnlyIn(t, files, "internal/fix")
	want := []string{"Build", "T.method", "onlyTests", "recursive"}
	if !slices.Equal(got, want) {
		t.Errorf("test-only functions = %q, want %q", got, want)
	}
}

// A folder whose every file this machine's build leaves out is no package
// go list names, and a file there may call the function.
func TestFindTestOnly_AFolderTheBuildLeavesOutKeepsExportedFunctions(t *testing.T) {
	files := maps.Clone(internalModule)
	files["tools/tools.go"] = "//go:build windows\n\npackage tools\n\nimport \"example.com/m/internal/fix\"\n\nvar _ = fix.Build\n"
	got := testOnlyIn(t, files, "internal/fix")
	want := []string{"T.method", "onlyTests", "recursive"}
	if !slices.Equal(got, want) {
		t.Errorf("test-only functions = %q, want %q", got, want)
	}
}

// A folder of the module the tool cannot read could hold anything, so the
// exported functions keep their bugs, and the run goes on.
func TestFindTestOnly_AFolderItCannotReadKeepsExportedFunctions(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every folder")
	}
	files := maps.Clone(internalModule)
	files["data/pg/README"] = "a database volume\n"
	got := testOnlyInAfter(t, files, "internal/fix", func(root string) {
		locked := filepath.Join(root, "data", "pg")
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o755) })
	})
	want := []string{"T.method", "onlyTests", "recursive"}
	if !slices.Equal(got, want) {
		t.Errorf("test-only functions = %q, want %q", got, want)
	}
}
