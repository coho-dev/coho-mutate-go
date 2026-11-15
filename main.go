// Command coho-mutate-go plants bugs in one Go package, one at a time, runs
// the package's tests against each, and writes the open mutation-testing
// report the Stryker testers write, listing every test that caught each bug.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const usage = `usage: coho-mutate-go [-workers n] [-since rev] <package-dir> <report-file>
       coho-mutate-go -version

Plants bugs in the Go package at <package-dir>, runs its tests against each,
and writes the mutation-testing report to <report-file>. File paths in the
report are relative to the directory it is run from.

-since plants bugs only on the lines ` + "`git diff <rev>`" + ` adds or changes in
the package, in files git does not track yet, and across the whole of a
source file whose test file it changes. <rev> is a revision, such as HEAD for
work not yet committed, or A... for the commit where HEAD split off A, such
as origin/main... for a pull request's change. Either way the diff runs to
the files on disk. A package it did not touch gets an empty report, and no
test runs.

A comment on the line before some code skips the kinds of bug it names there,
which the report marks Ignored, giving the comment's reason:

  //mutate:ignore EqualityOperator: either way the cache misses, so no test can tell

-version prints the version the binary was built at, and nothing else runs.

Exits 0 once the report is written or the version printed, 1 when the run
cannot finish, and 2 for a command line it cannot read. A planted bug that
made the tests hang is marked Timeout in the report and listed in the
printout, and the run still exits 0. The printout also lists each line
holding a bug no test caught: one marked Survived or NoCoverage.
`

// run reads the command line, mutates the package it names, writes the
// report and prints what the run found, or with -version prints the version
// alone. It answers the exit code.
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("coho-mutate-go", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, usage) }
	workers := flags.Int("workers", max(1, runtime.NumCPU()/2), "how many bugs are tested at once")
	since := flags.String("since", "", "plant bugs only where git diff against this revision touched")
	version := flags.Bool("version", false, "print the version and exit")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *version {
		fmt.Fprintln(stdout, moduleVersion(debug.ReadBuildInfo()))
		return 0
	}
	if flags.NArg() != 2 || *workers < 1 {
		flags.Usage()
		return 2
	}
	dir, reportPath := flags.Arg(0), flags.Arg(1)
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "coho-mutate-go:", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	result, err := Mutate(ctx, Options{Dir: dir, Root: root, Workers: *workers, Since: *since})
	if err != nil {
		fmt.Fprintln(stderr, "coho-mutate-go:", err)
		return 1
	}
	report, _ := json.MarshalIndent(result.Report, "", "  ")
	if err := os.WriteFile(reportPath, append(report, '\n'), 0o644); err != nil {
		fmt.Fprintln(stderr, "coho-mutate-go: write the report:", err)
		return 1
	}

	printResult(stdout, result, dir, reportPath, *workers)
	return 0
}

// moduleVersion is the main module's version as Go recorded it in the
// binary, or (devel), Go's own word for a build it recorded no version for.
func moduleVersion(info *debug.BuildInfo, ok bool) string {
	if !ok || info.Main.Version == "" {
		return "(devel)"
	}
	return info.Main.Version
}

// printResult says what the run found, listing each bug that made tests
// hang, where it is and which tests were running, then each line holding a
// bug no test caught.
func printResult(stdout io.Writer, result Result, dir, reportPath string, workers int) {
	r := result.Report
	fmt.Fprintf(stdout, "planted %d bugs in %s with %d workers\n", result.Planted, dir, workers)
	fmt.Fprintf(stdout, "  left out %d that did not compile\n", result.LeftOut)
	fmt.Fprintf(stdout, "  %d in the report: %d killed, %d timed out, %d survived, %d no test reaches, %d crashed the tests with no test to blame, %d skipped by a marker\n",
		result.Planted-result.LeftOut, r.Count(Killed), r.Count(Timeout), r.Count(Survived), r.Count(NoCoverage), r.Count(RuntimeError), r.Count(Ignored))
	fmt.Fprintf(stdout, "took %s (the tests alone, before any bug: %s)\n", result.Took.Round(time.Second/10), result.Baseline.Round(time.Second/10))
	fmt.Fprintf(stdout, "report written to %s\n", reportPath)
	var hangs []string
	for _, file := range keysOf(r.Files) {
		for _, m := range r.Files[file].Mutants {
			if m.Status == Timeout {
				hangs = append(hangs, fmt.Sprintf("  %s:%d %s %q: %s", file, m.Location.Start.Line, m.MutatorName, m.Replacement, m.StatusReason))
			}
		}
	}
	if len(hangs) > 0 {
		fmt.Fprintf(stdout, "%d %s made tests hang:\n", len(hangs), plural(len(hangs), "bug", "bugs"))
		for _, h := range hangs {
			fmt.Fprintln(stdout, h)
		}
	}
	if lines := uncaughtLines(r); len(lines) > 0 {
		fmt.Fprintf(stdout, "%d %s bugs no test caught:\n", len(lines), plural(len(lines), "line holds", "lines hold"))
		for _, l := range lines {
			fmt.Fprintln(stdout, l)
		}
	}
}

// uncaughtLines is each line holding a bug that Survived or that no test
// reaches, sorted by file and then by line. Each reads file:line, then every
// such bug on the line in the report's order, as the code it replaced and
// what it put there.
func uncaughtLines(r Report) []string {
	var out []string
	for _, file := range keysOf(r.Files) {
		f := r.Files[file]
		bugs := map[int][]string{}
		for _, m := range f.Mutants {
			if m.Status != Survived && m.Status != NoCoverage {
				continue
			}
			bug := replaced(f.Source, m.Location) + " → " + m.Replacement
			if m.Status == NoCoverage {
				bug += " (no test reaches)"
			}
			line := m.Location.Start.Line
			bugs[line] = append(bugs[line], bug)
		}
		lines := slices.Sorted(maps.Keys(bugs))
		for _, line := range lines {
			out = append(out, fmt.Sprintf("  %s:%d  %s", file, line, strings.Join(bugs[line], ", ")))
		}
	}
	return out
}

// replaced is the text of src that loc spans.
func replaced(src string, loc Location) string {
	return src[offset(src, loc.Start):offset(src, loc.End)]
}

// offset is the byte offset of p in src, the inverse of position.
func offset(src string, p Position) int {
	i := 0
	for range p.Line - 1 {
		i += strings.IndexByte(src[i:], '\n') + 1
	}
	for range p.Column - 1 {
		_, size := utf8.DecodeRuneInString(src[i:])
		i += size
	}
	return i
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// keysOf is the map's keys, sorted.
func keysOf[V any](m map[string]V) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}
