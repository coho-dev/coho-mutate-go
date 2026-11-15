package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"math"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// scope is the part of a package a run covers: the files its report lists,
// and the lines in each that bugs may be planted on. A file can be listed with
// no such lines, as one a diff only took lines out of is. Everything outside
// its methods, widen and the tests included, goes through them, so how the
// map holds a listed file is known here alone.
type scope struct {
	// lines is keyed by each listed file's name in the package's directory.
	// Each hunk of a diff is a range of its own, so two hunks next to each
	// other never read as one.
	lines map[string][]lineRange
}

func newScope() scope { return scope{lines: map[string][]lineRange{}} }

// add lists the file, and lets bugs go on the given lines of it. With no
// lines, it lists the file and lets none go on it.
func (s scope) add(name string, ranges ...lineRange) {
	s.lines[name] = append(s.lines[name], ranges...)
}

// whole lists the file and lets bugs go on every line of it.
func (s scope) whole(name string) { s.lines[name] = []lineRange{wholeFile} }

// files walks each listed file, with the lines in it bugs may go on.
func (s scope) files() iter.Seq2[string, []lineRange] { return maps.All(s.lines) }

// lists is whether the file is in the scope, and so in the report.
func (s scope) lists(name string) bool {
	_, ok := s.lines[name]
	return ok
}

// touches is whether a bug may go on the line, counted from one, of the
// named file.
func (s scope) touches(name string, line int) bool {
	for _, r := range s.lines[name] {
		if r.first <= line && line <= r.last {
			return true
		}
	}
	return false
}

// lineRange is the lines first to last of a file, both included.
type lineRange struct{ first, last int }

// wholeFile is a range holding every line of a file.
var wholeFile = lineRange{1, math.MaxInt}

// readChange runs `git diff -U0 <since>` in the package's directory and answers
// the scope it names: each file it touched, and the lines in it. since is a revision, or
// A... for the commit where HEAD split off A. Either way the diff runs to the
// files on disk, so its line numbers are those of the files bugs are planted
// in. A file git does not track yet is touched whole, and a test file the
// change touched touches the whole of the source file it goes with.
func readChange(ctx context.Context, pkg goPackage, since string) (scope, error) {
	// A revision git would read as an option is refused, not passed on.
	if strings.HasPrefix(since, "-") {
		return scope{}, fmt.Errorf("%q is no git revision", since)
	}
	// Resolved to one commit, every range git knows, such as A..B or X^!,
	// is refused: it ends at a commit, whose line numbers need not be those
	// of the files on disk.
	resolve := []string{"rev-parse", "--verify", "--quiet", "--end-of-options", since + "^{commit}"}
	if a, ok := strings.CutSuffix(since, "..."); ok && a != "" {
		resolve = []string{"merge-base", "--end-of-options", a, "HEAD"}
	}
	out, err := git(ctx, pkg.Dir, resolve...)
	if err != nil {
		return scope{}, fmt.Errorf("-since %s: give a revision, or A... for where HEAD split off A: %w", since, err)
	}
	from := strings.TrimSpace(string(out))
	// --relative names each file from the package's directory, as go list
	// does, wherever the module sits in the repository. The settings a
	// user's git config could change are each set here. --no-renames reads a
	// moved file as deleted and added, so every line of it is touched.
	out, err = git(ctx, pkg.Dir, "-c", "core.quotePath=false",
		"diff", "-U0", "--inter-hunk-context=0", "--relative", "--no-prefix", "--no-renames", "--no-color", "--no-ext-diff", "--no-textconv",
		from, "--", ".")
	if err != nil {
		return scope{}, err
	}
	c, err := parseDiff(out)
	if err != nil {
		return scope{}, fmt.Errorf("git diff %s in %s: %w", from, pkg.Dir, err)
	}
	// git diff leaves out the files git does not track yet.
	untracked, err := git(ctx, pkg.Dir, "ls-files", "-z", "--others", "--exclude-standard", "--", ".")
	if err != nil {
		return scope{}, err
	}
	for _, name := range strings.Split(string(untracked), "\x00") {
		if name != "" {
			c.whole(name)
		}
	}
	return widen(c, pkg), nil
}

// git runs git in dir and answers what it printed.
func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("git %s in %s: %s", strings.Join(args, " "), dir, strings.TrimSpace(stderr.String()))
		}
		return nil, err
	}
	return out, nil
}

// widen adds, for each test file in the package's directory the change
// touched, the whole of the source file of the same name, foo.go for
// foo_test.go: a changed test can stop catching a bug anywhere in it. That
// holds for a test file the change only took lines out of, or deleted. A test
// file with no source file of its name, such as one of helpers, adds every
// source file in the package. A test file this machine does not build, or
// one whose source file it does not build, adds nothing: it tests nothing
// built here.
func widen(c scope, pkg goPackage) scope {
	out := newScope()
	for name, ranges := range c.files() {
		out.add(name, ranges...)
	}
	for name := range c.files() {
		if !strings.HasSuffix(name, "_test.go") || strings.Contains(name, "/") {
			continue
		}
		source := strings.TrimSuffix(name, "_test.go") + ".go"
		if slices.Contains(pkg.IgnoredGoFiles, name) || slices.Contains(pkg.IgnoredGoFiles, source) {
			continue
		}
		sources := pkg.GoFiles
		if slices.Contains(pkg.GoFiles, source) {
			sources = []string{source}
		}
		for _, s := range sources {
			out.whole(s)
		}
	}
	return out
}

// hunkHeader is a -U0 hunk's first line: @@ -start[,count] +start[,count] @@.
var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// parseDiff reads git's -U0 diff, with paths relative to the package's
// directory and no a/ and b/ prefixes, into the lines it adds or changes in
// each file. A file whose hunks only delete lines, or that the diff deletes,
// is listed with no lines.
func parseDiff(diff []byte) (scope, error) {
	c := newScope()
	// file is the file the current hunks change, named under its old name
	// when the diff deletes it, and empty until a file is named.
	file, old := "", ""
	// left is how many lines of the current hunk are still to come. They are
	// skipped, since an added line can itself start "+++ " or "@@ ".
	left := 0
	var err error
	scanner := bufio.NewScanner(bytes.NewReader(diff))
	scanner.Buffer(nil, 1<<30)
	for scanner.Scan() {
		line := scanner.Text()
		if left > 0 {
			if !strings.HasPrefix(line, `\`) {
				left--
			}
			continue
		}
		if name, ok := strings.CutPrefix(line, "--- "); ok {
			if old, err = fileName(name); err != nil {
				return scope{}, err
			}
			continue
		}
		if name, ok := strings.CutPrefix(line, "+++ "); ok {
			if file, err = fileName(name); err != nil {
				return scope{}, err
			}
			if file == "" {
				file = old
			}
			continue
		}
		if !strings.HasPrefix(line, "@@") {
			continue
		}
		m := hunkHeader.FindStringSubmatch(line)
		if m == nil {
			return scope{}, fmt.Errorf("cannot read the hunk header %q", line)
		}
		if file == "" {
			return scope{}, fmt.Errorf("the hunk %q names no file", line)
		}
		removed, start, added := count(m[1]), count(m[2]), count(m[3])
		left = removed + added
		if added > 0 {
			c.add(file, lineRange{start, start + added - 1})
		} else {
			c.add(file)
		}
	}
	if err := scanner.Err(); err != nil {
		return scope{}, err
	}
	if left > 0 {
		return scope{}, fmt.Errorf("the diff ends %d lines into a hunk of %s", left, file)
	}
	return c, nil
}

// fileName reads a file's name from a --- or +++ line, unquoting the C-style
// quotes git puts around an unusual one, and dropping the tab git ends the
// line with when an unquoted name holds a space. /dev/null, the side of a
// diff where a file does not exist, reads as empty.
func fileName(name string) (string, error) {
	name = strings.TrimSuffix(name, "\t")
	if name == "/dev/null" {
		return "", nil
	}
	if !strings.HasPrefix(name, `"`) {
		return name, nil
	}
	unquoted, err := strconv.Unquote(name)
	if err != nil {
		return "", fmt.Errorf("cannot read the file name %s", name)
	}
	return unquoted, nil
}

// count reads a number the hunk header matched, where a count left out is one.
func count(s string) int {
	if s == "" {
		return 1
	}
	n, _ := strconv.Atoi(s)
	return n
}
