package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Options says which package to mutate and how.
type Options struct {
	// Dir is the package's directory.
	Dir string
	// Root is the directory the report's file paths are relative to. Empty
	// means Dir.
	Root string
	// Workers is how many planted bugs are tested at once. Zero means one.
	Workers int
	// Timeout is how long the test binary may run against one bug. Zero
	// means ten times as long as the tests took before any bug was planted,
	// and at least 30 seconds.
	Timeout time.Duration
	// Since, when set, is a git revision, or A... for the commit where HEAD
	// split off A. Bugs are planted only on the lines the diff from it to
	// the files on disk adds or changes in the package, in files git does
	// not track yet, and across the whole of a source file whose test file
	// the diff changes. Empty plants bugs everywhere.
	Since string
}

// Result is what one run found.
type Result struct {
	// Report holds every planted bug that compiled.
	Report Report
	// Planted counts every bug planted, including those that did not compile.
	Planted int
	// LeftOut counts the planted bugs that did not compile, which the report
	// leaves out.
	LeftOut int
	// Baseline is how long the package's tests took before any bug was
	// planted, coverage and build included. It is zero when no test ran,
	// as when a Since change planted no bug.
	Baseline time.Duration
	// Took is how long the whole run took.
	Took time.Duration
}

// goPackage is what `go list -json` says about a package.
type goPackage struct {
	Dir        string
	ImportPath string
	// Name is the package's name, as its package clause gives it.
	Name    string
	GoFiles []string
	// CgoFiles are the source files that import "C", which GoFiles leaves
	// out.
	CgoFiles     []string
	TestGoFiles  []string
	XTestGoFiles []string
	// IgnoredGoFiles are the files build constraints leave out on this
	// machine, such as foo_windows.go on Linux.
	IgnoredGoFiles []string
	// Imports, TestImports and XTestImports are the import paths the
	// source files, the in-package test files and the external test files
	// import.
	Imports, TestImports, XTestImports []string
	Module                             struct{ Path, Dir string }
	// Error is why go list could not read the package, when it could not.
	Error *struct{ Err string }
}

// sourceFile is one of the package's files and the bugs planted in it.
type sourceFile struct {
	path    string
	src     []byte
	mutants []Mutant
	// firstID is the report id of the file's first mutant. Ids count every
	// planted bug, so a bug's id does not move when another stops compiling.
	firstID int
}

// Mutate plants every bug it can in the package at opts.Dir, or with
// opts.Since every bug on the lines that change touched, tests each one, and
// reports which tests caught it. It refuses a package with no tests, and one
// whose tests fail before any bug is planted, unless the change planted
// nothing there.
func Mutate(ctx context.Context, opts Options) (Result, error) {
	start := time.Now()
	pkg, err := listPackage(ctx, opts.Dir)
	if err != nil {
		return Result{}, err
	}
	// touched is the run's scope: the files the report lists, and the lines
	// in them bugs may be planted on. That is every line of every file,
	// unless a change names fewer.
	touched := newScope()
	for _, name := range pkg.GoFiles {
		touched.whole(name)
	}
	if opts.Since != "" {
		if touched, err = readChange(ctx, pkg, opts.Since); err != nil {
			return Result{}, err
		}
	}
	root := opts.Root
	if root == "" {
		root = pkg.Dir
	}
	report := Report{
		SchemaVersion: "2",
		Thresholds:    Thresholds{High: 80, Low: 60},
		ProjectRoot:   root,
		TestFiles:     map[string]TestFile{},
		Framework:     Framework{Name: "coho-mutate-go"},
	}
	var only testOnly
	if slices.ContainsFunc(pkg.GoFiles, touched.lists) {
		if only, err = findTestOnly(ctx, pkg); err != nil {
			return Result{}, err
		}
	}
	var files []sourceFile
	planted := 0
	for _, name := range pkg.GoFiles {
		if !touched.lists(name) {
			continue
		}
		path := filepath.Join(pkg.Dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			return Result{}, err
		}
		mutants, err := Plant(src, only)
		if err != nil {
			return Result{}, fmt.Errorf("plant bugs in %s: %w", path, err)
		}
		mutants = slices.DeleteFunc(mutants, func(m Mutant) bool {
			return !touched.touches(name, position(src, m.Start).Line)
		})
		files = append(files, sourceFile{path: path, src: src, mutants: mutants, firstID: planted + 1})
		planted += len(mutants)
	}
	// The one place the -since mode differs beyond its scope: a change that
	// planted no bug here needs no test run, so a check can name every
	// package and pay only for those the change touched. A run without it
	// tests even a package with no bug, as it always has, which refuses one
	// with no tests.
	if opts.Since != "" && planted == 0 {
		report.Files, _ = fileResults(root, files, nil)
		return Result{Report: report, Took: time.Since(start)}, nil
	}
	testFiles := append(append([]string{}, pkg.TestGoFiles...), pkg.XTestGoFiles...)
	testFileOf, err := testFunctions(pkg.Dir, testFiles)
	if err != nil {
		return Result{}, err
	}
	// A test file can hold only helpers, and then no test would run.
	if len(testFileOf) == 0 {
		return Result{}, fmt.Errorf("%s has no tests", opts.Dir)
	}
	setup := time.Since(start)

	work, err := os.MkdirTemp("", "coho-mutate-go-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(work)

	baseStart := time.Now()
	profile := filepath.Join(work, "cover.out")
	base, err := tester{dir: pkg.Dir, timeout: 10 * time.Minute}.run(ctx, "", selection{}, "-covermode=set", "-coverprofile="+profile)
	if err != nil {
		return Result{}, err
	}
	switch base.Ending {
	case EndedPassing:
	case EndedUnbuilt:
		return Result{}, fmt.Errorf("the tests in %s do not compile before any bug is planted:\n%s", opts.Dir, strings.TrimSpace(base.BuildOutput))
	case EndedFailing:
		return Result{}, fmt.Errorf("the tests in %s fail before any bug is planted: %v", opts.Dir, leaves(base.Failed))
	default:
		return Result{}, fmt.Errorf("the tests in %s %s before any bug is planted, in %v", opts.Dir, map[Ending]string{EndedStopped: "crash", EndedTimedOut: "hang"}[base.Ending], append(leaves(base.Failed), base.Unfinished...))
	}
	baseline := time.Since(baseStart)
	coverFile, err := os.Open(profile)
	if err != nil {
		return Result{}, err
	}
	cov, err := readCoverage(coverFile)
	coverFile.Close()
	if err != nil {
		return Result{}, err
	}
	var want []string
	for name := range base.Passed {
		if topLevel(name) == name {
			want = append(want, name)
		}
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = max(30*time.Second, 10*base.Elapsed)
	}

	mutationStart := time.Now()
	t := tester{dir: pkg.Dir, timeout: timeout}
	catches, err := catchAll(ctx, t, work, files, cov, want, max(1, opts.Workers))
	if err != nil {
		return Result{}, err
	}
	mutation := time.Since(mutationStart)

	report.Performance = Performance{Setup: ms(setup), InitialRun: ms(baseline), Mutation: ms(mutation)}
	var leftOut int
	report.Files, leftOut = fileResults(root, files, catches)
	for name := range base.Passed {
		file := relPath(root, filepath.Join(pkg.Dir, testFileOf[topLevel(name)]))
		tf := report.TestFiles[file]
		tf.Tests = append(tf.Tests, TestDefinition{ID: name, Name: name})
		report.TestFiles[file] = tf
	}
	for file, tf := range report.TestFiles {
		sortTests(tf.Tests)
		report.TestFiles[file] = tf
	}
	return Result{Report: report, Planted: planted, LeftOut: leftOut, Baseline: baseline, Took: time.Since(start)}, nil
}

// fileResults is the report's entry for each file, named from root, with how
// many bugs it left out because they did not compile. catches is indexed as
// files and their mutants are, and may be nil where no file has a mutant.
func fileResults(root string, files []sourceFile, catches [][]*Catch) (map[string]FileResult, int) {
	results := map[string]FileResult{}
	leftOut := 0
	for fi, f := range files {
		result := FileResult{Language: "go", Source: string(f.src), Mutants: []MutantResult{}}
		for mi, m := range f.mutants {
			c := catches[fi][mi]
			if c.Status == CompileError {
				leftOut++
				continue
			}
			result.Mutants = append(result.Mutants, MutantResult{
				ID:           strconv.Itoa(f.firstID + mi),
				MutatorName:  m.Mutator,
				Replacement:  m.Replacement,
				Location:     Location{Start: position(f.src, m.Start), End: position(f.src, m.End)},
				Status:       c.Status,
				KilledBy:     c.Killers,
				StatusReason: reason(m, c),
				Duration:     ms(c.Elapsed),
			})
		}
		results[relPath(root, f.path)] = result
	}
	return results, leftOut
}

// catchAll tests every mutant, workers at a time. A mutant no test reaches
// gets a Catch without a run. The answer is indexed as files and their
// mutants are.
func catchAll(ctx context.Context, t tester, work string, files []sourceFile, cov coverage, want []string, workers int) ([][]*Catch, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	covs := make([]fileCoverage, len(files))
	for i, f := range files {
		var err error
		if covs[i], err = cov.file(f.path, f.src); err != nil {
			return nil, err
		}
	}
	catches := make([][]*Catch, len(files))
	type job struct{ file, mutant int }
	jobs := make(chan job)
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				c, err := catchOne(ctx, t, work, files[j.file], j.mutant, covs[j.file], want)
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = err
					cancel()
				}
				catches[j.file][j.mutant] = c
				mu.Unlock()
			}
		}()
	}
feed:
	for fi, f := range files {
		catches[fi] = make([]*Catch, len(f.mutants))
		for mi := range f.mutants {
			select {
			case jobs <- job{fi, mi}:
			case <-ctx.Done():
				break feed
			}
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return catches, nil
}

// catchOne tests one mutant, planting it through go's -overlay so the
// package's own files are never touched. A mutant no test reaches is only
// built, to leave it out if it does not compile.
func catchOne(ctx context.Context, t tester, work string, f sourceFile, mi int, cov fileCoverage, want []string) (*Catch, error) {
	m := f.mutants[mi]
	if m.Ignored != "" {
		return &Catch{Status: Ignored}, nil
	}
	dir, err := os.MkdirTemp(work, "mutant-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	mutated := filepath.Join(dir, filepath.Base(f.path))
	if err := os.WriteFile(mutated, m.Apply(f.src), 0o644); err != nil {
		return nil, err
	}
	overlay, _ := json.Marshal(map[string]map[string]string{"Replace": {f.path: mutated}})
	overlayPath := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(overlayPath, overlay, 0o644); err != nil {
		return nil, err
	}
	if cov.unreached(m.Start) {
		ok, err := t.builds(ctx, overlayPath)
		if err != nil {
			return nil, err
		}
		if !ok {
			return &Catch{Status: CompileError}, nil
		}
		return &Catch{Status: NoCoverage}, nil
	}
	c, err := t.catch(ctx, overlayPath, want)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func listPackage(ctx context.Context, dir string) (goPackage, error) {
	pkgs, err := goList(ctx, dir, ".")
	if err != nil {
		return goPackage{}, err
	}
	return pkgs[0], nil
}

// goList is what go list, run in dir, says about each package the pattern
// names.
func goList(ctx context.Context, dir string, pattern string, flags ...string) ([]goPackage, error) {
	var fields []string
	for f := range reflect.TypeFor[goPackage]().Fields() {
		fields = append(fields, f.Name)
	}
	args := append([]string{"list", "-json=" + strings.Join(fields, ",")}, flags...)
	cmd := exec.CommandContext(ctx, "go", append(args, pattern)...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("go list in %s: %s", dir, strings.TrimSpace(stderr.String()))
		}
		return nil, err
	}
	var pkgs []goPackage
	for dec := json.NewDecoder(bytes.NewReader(out)); dec.More(); {
		var pkg goPackage
		if err := dec.Decode(&pkg); err != nil {
			return nil, fmt.Errorf("read go list's answer: %w", err)
		}
		pkgs = append(pkgs, pkg)
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("go list in %s named no package", dir)
	}
	return pkgs, nil
}

// testFunctions maps each top-level test, example and fuzz test in the test
// files to the file declaring it.
func testFunctions(dir string, names []string) (map[string]string, error) {
	fileOf := map[string]string{}
	fset := token.NewFileSet()
	for _, name := range names {
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			for _, prefix := range []string{"Test", "Example", "Fuzz"} {
				if !strings.HasPrefix(fn.Name.Name, prefix) {
					continue
				}
				// The package and its external test package can each declare
				// one, and go test's events would name both the same.
				if other, ok := fileOf[fn.Name.Name]; ok {
					return nil, fmt.Errorf("%s is declared in both %s and %s, so the report could not say which caught a bug: rename one", fn.Name.Name, other, name)
				}
				fileOf[fn.Name.Name] = name
			}
		}
	}
	return fileOf, nil
}

// position is the report's line and column for a byte offset: both count
// from one, and the column counts characters.
func position(src []byte, offset int) Position {
	lineStart := bytes.LastIndexByte(src[:offset], '\n') + 1
	return Position{
		Line:   bytes.Count(src[:offset], []byte("\n")) + 1,
		Column: utf8.RuneCount(src[lineStart:offset]) + 1,
	}
}

func relPath(root, path string) string {
	rel, _ := filepath.Rel(root, path)
	return filepath.ToSlash(rel)
}

func ms(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}

// reason is the report's statusReason for a bug, where its status needs
// one: a skipped bug gives its marker's reason, and a bug that made a test
// hang names the tests that were running.
func reason(m Mutant, c *Catch) string {
	if c.Status == Ignored {
		return m.Ignored
	}
	const after = ", so the tests after it were not run against this bug"
	switch {
	case c.Status != Timeout:
		return ""
	case len(c.Hung) == 0:
		return "the package hung before any test started"
	case len(c.Hung) == 1:
		return c.Hung[0] + " hung" + after
	default:
		return "one of " + strings.Join(c.Hung, ", ") + " hung (they were running in parallel)" + after
	}
}
