package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Ending is how one run of the test binary ended.
type Ending string

const (
	// EndedPassing and EndedFailing are a binary that ran every test it was
	// given, which then prints PASS or FAIL itself.
	EndedPassing Ending = "passing"
	EndedFailing Ending = "failing"
	// EndedUnbuilt is a package or tests that did not compile.
	EndedUnbuilt Ending = "unbuilt"
	// EndedStopped is a binary stopped part way, by a test that panicked or
	// exited it.
	EndedStopped Ending = "stopped"
	// EndedTimedOut is a binary its timeout stopped.
	EndedTimedOut Ending = "timed out"
)

// stopped is whether the binary stopped before its tests were done.
func (e Ending) stopped() bool { return e == EndedStopped || e == EndedTimedOut }

// TestRun is what one `go test -json` run of the package said.
type TestRun struct {
	// Ending is how the run ended, or empty when go test reported no result
	// for the package.
	Ending Ending
	// Passed and Failed are every test that finished, by full name, subtests
	// included. A skipped test counts as passed: it caught nothing.
	Passed, Failed map[string]bool
	// Unfinished is every test that was running, not paused, when the test
	// binary stopped, sorted. The binary stops inside a test that exits it,
	// inside a test that runs past the timeout, and in every parallel test
	// running beside one that panics.
	Unfinished []string
	// LastStarted is the test that started last. When a test exits the
	// binary, it is the one that did. After a panic, the events that follow
	// name only its parents, and a parallel test may have started since.
	LastStarted string
	// BuildOutput is what the compiler said, when the package or its tests
	// did not compile.
	BuildOutput string
	// Elapsed is the test binary's own time, as go test reports it.
	Elapsed time.Duration
}

// testEvent is one line `go test -json` prints, as cmd/test2json documents it.
type testEvent struct {
	Action      string
	Test        string
	Elapsed     float64
	Output      string
	FailedBuild string
}

// readTestRun reads the events `go test -json` prints for one package.
func readTestRun(r io.Reader) (TestRun, error) {
	run := TestRun{Passed: map[string]bool{}, Failed: map[string]bool{}}
	running := map[string]bool{}
	ended, failed, completed, unbuilt, timedOut := false, false, false, false, false
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev testEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return TestRun{}, fmt.Errorf("read a go test event: %w", err)
		}
		switch ev.Action {
		case "build-output":
			run.BuildOutput += ev.Output
		case "build-fail":
			unbuilt = true
		case "output":
			// The testing package's own timer starts only once the package has
			// loaded, so a hang before then is go test killing the binary.
			if strings.HasPrefix(ev.Output, "panic: test timed out") || strings.HasPrefix(ev.Output, "*** Test killed with quit: ran too long") {
				timedOut = true
			}
			if ev.Test == "" && (ev.Output == "PASS\n" || ev.Output == "FAIL\n") {
				completed = true
			}
		}
		if ev.Test == "" {
			if ev.Action == "pass" || ev.Action == "fail" {
				ended = true
				failed = ev.Action == "fail"
				run.Elapsed = time.Duration(ev.Elapsed * float64(time.Second))
				if ev.FailedBuild != "" {
					unbuilt = true
				}
			}
			continue
		}
		switch ev.Action {
		case "run", "cont":
			// A PASS or FAIL line before a test starts was printed by the
			// package, not by the binary at its end.
			completed = false
			run.LastStarted = ev.Test
			running[ev.Test] = true
		case "pause":
			delete(running, ev.Test)
		case "pass", "skip":
			delete(running, ev.Test)
			run.Passed[ev.Test] = true
		case "fail":
			delete(running, ev.Test)
			run.Failed[ev.Test] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return TestRun{}, fmt.Errorf("read go test's output: %w", err)
	}
	for name := range running {
		run.Unfinished = append(run.Unfinished, name)
	}
	slices.Sort(run.Unfinished)
	switch {
	case unbuilt:
		run.Ending = EndedUnbuilt
	case !ended:
	case !failed:
		run.Ending = EndedPassing
	case completed && len(run.Unfinished) == 0:
		run.Ending = EndedFailing
	case timedOut:
		run.Ending = EndedTimedOut
	default:
		run.Ending = EndedStopped
	}
	return run, nil
}

// leaves is every name in names with no subtest of its own in names, sorted.
// A parent fails whenever one of its subtests does, so it is the subtest that
// caught the bug.
func leaves(names map[string]bool) []string {
	var found []string
	for name := range names {
		leaf := true
		for other := range names {
			if strings.HasPrefix(other, name+"/") {
				leaf = false
				break
			}
		}
		if leaf {
			found = append(found, name)
		}
	}
	slices.Sort(found)
	return found
}

// topLevel is the name of the top-level test a test belongs to.
func topLevel(name string) string {
	top, _, _ := strings.Cut(name, "/")
	return top
}

// anyOf is a pattern matching exactly the names, or empty when there are
// none.
func anyOf(names []string) string {
	if len(names) == 0 {
		return ""
	}
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = regexp.QuoteMeta(n)
	}
	slices.Sort(quoted)
	return "^(" + strings.Join(quoted, "|") + ")$"
}

// exactly is the -run pattern selecting the one test with this full name,
// matching each level of its name in turn.
func exactly(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = "^" + regexp.QuoteMeta(p) + "$"
	}
	return strings.Join(parts, "/")
}

// selection is which tests one run runs, as go test's -run and -skip
// patterns. Empty means no flag.
type selection struct {
	run, skip string
}

// Catch is what the package's tests did with one planted bug.
type Catch struct {
	// Status is the report's word for it. CompileError is a bug the report
	// leaves out.
	Status Status
	// Killers is every test that caught the bug, sorted. On a bug that made
	// a test hang, it holds only the tests that caught it until then.
	Killers []string
	// Hung is the tests running when the bug made the binary hang, sorted.
	// The search stops at the first hang.
	Hung []string
	// Elapsed is the test binary's own time, summed over every run it took.
	Elapsed time.Duration
}

// tester runs one package's tests.
type tester struct {
	dir     string
	timeout time.Duration
}

// run runs the package's tests once. overlay is the path of a -overlay file,
// or empty; extra is passed on to go test.
func (t tester) run(ctx context.Context, overlay string, sel selection, extra ...string) (TestRun, error) {
	args := []string{"test", "-json", "-count=1", "-vet=off", "-timeout=" + t.timeout.String()}
	if overlay != "" {
		args = append(args, "-overlay="+overlay)
	}
	if sel.run != "" {
		args = append(args, "-run="+sel.run)
	}
	if sel.skip != "" {
		args = append(args, "-skip="+sel.skip)
	}
	args = append(args, extra...)
	args = append(args, ".")
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = t.dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return TestRun{}, ctx.Err()
	}
	run, readErr := readTestRun(&stdout)
	if readErr != nil {
		return TestRun{}, readErr
	}
	var exit *exec.ExitError
	if run.Ending == "" || err != nil && !(errors.As(err, &exit) && run.Ending != EndedPassing) {
		return TestRun{}, fmt.Errorf("go %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return run, nil
}

// builds says whether the package's tests compile with the overlay's bug in
// place, running none of them.
func (t tester) builds(ctx context.Context, overlay string) (bool, error) {
	run, err := t.run(ctx, overlay, selection{run: "^$"})
	return run.Ending != EndedUnbuilt, err
}

// catch runs every test in want against the bug the overlay plants. A test
// that panics, exits or hangs stops the test binary before later tests run,
// so catch keeps running what is left, as catcher says, until every test in
// want has run. A hang ends it at once, since each further run could wait
// the whole timeout again.
func (t tester) catch(ctx context.Context, overlay string, want []string) (Catch, error) {
	c := &catcher{tester: t, overlay: overlay, killers: map[string]bool{}, isolated: map[string]bool{}}
	if err := c.resume(ctx, "", map[string]bool{}, want); err != nil {
		return Catch{}, err
	}
	if c.unbuilt {
		return Catch{Status: CompileError}, nil
	}
	return c.result(), nil
}

// catcher gathers what every run against one bug caught. Every run goes
// through try.
type catcher struct {
	tester
	overlay string
	killers map[string]bool
	elapsed time.Duration
	// isolated is every test already run alone against the bug. A test
	// whose own body stops the binary would otherwise be isolated again
	// each time its remaining subtests are resumed, for ever.
	isolated map[string]bool
	// timedOut is whether a run hung.
	timedOut bool
	// hung is the tests running when it did, which is none when the
	// package hung before any test started.
	hung []string
	// unbuilt is whether the bug did not compile.
	unbuilt bool
	// failed is whether any run failed as a whole, which a bug can make
	// happen with no test to blame.
	failed bool
}

// try runs the selection once against the bug, and adds up its time. It
// runs nothing once a run has hung, or once the bug is known not to compile,
// and answers false.
func (c *catcher) try(ctx context.Context, sel selection) (TestRun, bool, error) {
	if c.unbuilt {
		return TestRun{}, false, nil
	}
	if c.timedOut {
		return TestRun{}, false, nil
	}
	run, err := c.tester.run(ctx, c.overlay, sel)
	if err != nil {
		return TestRun{}, false, err
	}
	c.elapsed += run.Elapsed
	switch run.Ending {
	case EndedUnbuilt:
		c.unbuilt = true
		return run, false, nil
	case EndedTimedOut:
		c.timedOut = true
		c.hung = leaves(setOf(run.Unfinished))
	}
	c.failed = c.failed || run.Ending != EndedPassing
	return run, true, nil
}

// resume runs the tests directly under parent, or the package's top-level
// tests when parent is empty, skipping each one in ran, which it adds to. It
// runs again after each run the binary stopped part way, until a run reaches
// no test it had not, or every test in want has run.
func (c *catcher) resume(ctx context.Context, parent string, ran map[string]bool, want []string) error {
	for {
		var names []string
		for name := range ran {
			names = append(names, name)
		}
		sel := selection{skip: anyOf(names)}
		if parent != "" {
			sel = selection{run: exactly(parent)}
			if len(names) > 0 {
				sel.skip = exactly(parent) + "/" + anyOf(names)
			}
		}
		run, ok, err := c.runAll(ctx, sel, parent, "")
		if err != nil || !ok {
			return err
		}
		progress := false
		for name := range childrenOf(parent, run) {
			if !ran[name] {
				ran[name] = true
				progress = true
			}
		}
		if !run.Ending.stopped() || !progress || want != nil && everyDone(want, ran) {
			return nil
		}
	}
}

// runAll runs the selection and credits what it caught, as runOnce says.
// When the binary stopped part way, it resumes every level below within that
// the run left part done, as resumeStopped says.
func (c *catcher) runAll(ctx context.Context, sel selection, within, alone string) (TestRun, bool, error) {
	run, ok, err := c.runOnce(ctx, sel, alone)
	if err != nil || !ok || !run.Ending.stopped() {
		return run, ok, err
	}
	return run, true, c.resumeStopped(ctx, run, within)
}

// resumeStopped resumes, deepest first, each test below within that a
// stopped run may have left part done: the parents of the test that started
// last, of each test left unfinished, and of each test that failed, since a
// panicking test fails before the binary stops.
func (c *catcher) resumeStopped(ctx context.Context, run TestRun, within string) error {
	names := append([]string{run.LastStarted}, run.Unfinished...)
	names = append(names, leaves(run.Failed)...)
	parents := map[string]bool{}
	for _, name := range names {
		for p := name; strings.Contains(p, "/"); {
			p = p[:strings.LastIndex(p, "/")]
			parents[p] = true
		}
	}
	var levels []string
	for p := range parents {
		if within == "" || strings.HasPrefix(p, within+"/") {
			levels = append(levels, p)
		}
	}
	slices.SortFunc(levels, func(a, b string) int { return strings.Count(b, "/") - strings.Count(a, "/") })
	for _, p := range levels {
		if err := c.resume(ctx, p, childrenOf(p, run), nil); err != nil {
			return err
		}
	}
	return nil
}

// runOnce runs the selection once and credits every test that failed.
// alone is the test the selection runs on its own, or empty. A test still
// running when the binary stopped is credited only if it is alone: any other
// may only have been cut off by the test that stopped it, so it is isolated,
// run alone in turn, one level further down each time. On a timeout, the
// one test running is the one that hung, and with several, which one hung is
// unknown.
func (c *catcher) runOnce(ctx context.Context, sel selection, alone string) (TestRun, bool, error) {
	run, ok, err := c.try(ctx, sel)
	if err != nil || !ok {
		return run, ok, err
	}
	c.credit(under(alone, leaves(run.Failed)))
	if !run.Ending.stopped() {
		return run, true, nil
	}
	unfinished := under(alone, leaves(setOf(run.Unfinished)))
	if run.Ending == EndedTimedOut {
		if len(unfinished) == 1 {
			c.credit(unfinished)
		}
		return run, true, nil
	}
	for _, u := range unfinished {
		if u == alone {
			c.credit([]string{u})
			continue
		}
		if err := c.isolate(ctx, u); err != nil {
			return TestRun{}, false, err
		}
	}
	return run, true, nil
}

// isolate runs the test name alone, once per bug, and resumes what it left
// unrun when it stops the binary itself.
func (c *catcher) isolate(ctx context.Context, name string) error {
	if c.isolated[name] {
		return nil
	}
	c.isolated[name] = true
	_, _, err := c.runAll(ctx, selection{run: exactly(name)}, parentOf(name), name)
	return err
}

// under is each of names that is name or one of its subtests, or all of
// them when name is empty.
func under(name string, names []string) []string {
	if name == "" {
		return names
	}
	var found []string
	for _, n := range names {
		if n == name || strings.HasPrefix(n, name+"/") {
			found = append(found, n)
		}
	}
	return found
}

// parentOf is the name of the test a subtest runs under, or empty for a
// top-level test.
func parentOf(name string) string {
	i := strings.LastIndex(name, "/")
	if i < 0 {
		return ""
	}
	return name[:i]
}

// reached is every test the run started.
func reached(run TestRun) map[string]bool {
	all := setOf(run.Unfinished)
	for name := range run.Passed {
		all[name] = true
	}
	for name := range run.Failed {
		all[name] = true
	}
	return all
}

// childrenOf is the last part of the name of each of parent's direct
// subtests the run reached, or each top-level test when parent is empty.
func childrenOf(parent string, run TestRun) map[string]bool {
	children := map[string]bool{}
	for name := range reached(run) {
		rest, ok := name, true
		if parent != "" {
			rest, ok = strings.CutPrefix(name, parent+"/")
		}
		if ok {
			child, _, _ := strings.Cut(rest, "/")
			children[child] = true
		}
	}
	return children
}

func (c *catcher) credit(names []string) {
	for _, n := range names {
		c.killers[n] = true
	}
}

func (c *catcher) result() Catch {
	var killers []string
	for k := range c.killers {
		killers = append(killers, k)
	}
	slices.Sort(killers)
	status := Survived
	switch {
	case c.timedOut:
		status = Timeout
	case len(killers) > 0:
		status = Killed
	case c.failed:
		status = RuntimeError
	}
	return Catch{Status: status, Killers: killers, Hung: c.hung, Elapsed: c.elapsed}
}

func setOf(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func everyDone(want []string, done map[string]bool) bool {
	for _, w := range want {
		if !done[w] {
			return false
		}
	}
	return true
}
