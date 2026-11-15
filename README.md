# coho-mutate-go

A mutation tester for Go. It plants small bugs in one package, one at a time,
runs the package's tests against each, and lists every test that caught each
bug. The report is in Stryker's open mutation-testing report format.

```sh
go install github.com/coho-dev/coho-mutate-go@latest
coho-mutate-go [-workers n] [-since rev] <package-dir> <report-file>
coho-mutate-go -version
```

Install it with `go install` and run the installed binary. Don't run it with
`go run`: `go run` turns every failing exit into exit code 1, so a caller can't
tell a command line the tool could not read from a run that could not finish.

## Only the lines a change touched

A check on a pull request only cares about the code the pull request changed.
`-since` plants bugs only on the lines `git diff <rev>` adds or changes in the
package:

```sh
coho-mutate-go -since origin/main... ./internal/store report.json
```

- `<rev>` is a git revision, or `A...` for the commit where `HEAD` split off
  `A`. `origin/main...` covers the pull request's own commits, and not those
  merged into `main` since the branch was made. `HEAD` covers work not yet
  committed. Either way the diff runs to the files on disk, so edits not yet
  committed count too. Any other range, such as `A..B` or `HEAD^!`, is refused: it ends at
  a commit, whose line numbers need not match the files on disk.
- A file git does not track yet, or one the change moved, counts as touched
  on every line.
- A changed test file `foo_test.go` counts as touching every line of `foo.go`,
  since a changed test can stop catching a bug anywhere in the file it tests.
  That holds when the change only takes lines out of the test file, or
  deletes it. A test file this machine's build leaves out, such as
  `foo_windows_test.go` on Linux, adds nothing.
  A changed test file with no source file of its name, such as a file of
  helpers, counts as touching every source file in the package.
- A line the change only deleted is not there to plant a bug on, so it adds
  nothing.
- The report lists each source file the change touched, with no bugs where
  it touched no line that holds one, and no other file. When the change
  plants no bug in a package, its tests are not run, so a check can name
  every package and pay only for the ones the change touched.
- It works wherever the module sits in the repository: file names come from
  `go list` and from `git diff --relative`, run in the package's directory.

## Skipping a bug

A `//mutate:ignore` comment on the line before some code skips the kinds of
bug it names on that line, and gives the reason:

```go
//mutate:ignore EqualityOperator: either way the cache misses, so no test can tell
if len(cache) > limit {
```

The report marks each skipped bug Ignored. A comment that skips nothing stops
the run, so a stale one can't hide a bug added to its line later.

Four kinds of bug are never planted, because no test could catch them or
none should:

- **The `++` or `--` in a `for` loop's last clause**, as in
  `for i := 0; i < n; i++`. Counting the other way there usually never ends.
- **A `++` or `--` on a counter only compared with zero.** This is a local
  variable declared with the value 0, as in `n := 0` or `var n int`. After that
  it is only ever counted up, or only ever counted down. It is only read as
  `n == 0` or `n != 0`, and its address is never taken. Counting the other way
  gives -1 where it gave 1, and both are non-zero.
- **A `true` in a map literal whose every value is `true`**, as in
  `map[string]bool{".sh": true, ".bash": true}`. That map is a set, and a test
  for each member would only copy the list. A map literal with any other value
  keeps all its bugs.
- **Any bug in a function only test files call**, such as a fixture kept in a
  non-test file so the tests of other packages can use it too. A bug there can
  only make a test wrong, never the program. The function must be one nothing
  outside the module can call: unexported, or in an `internal` package whose
  parent folder is inside the module. A module nested under that parent folder
  can call it too when its module path is under that folder's import path, as
  a `v2` major version's is. So can a folder this machine's build leaves out
  whole, such as a Windows-only command on Linux. When the tool finds either,
  or a folder it can't read, only unexported functions count. Its name must
  appear in a `_test.go` file, here or in another package of the module, and
  in no other file, including a file this machine's build leaves out. A method
  that an interface could call keeps its bugs: an exported method, or one an
  interface in the package lists. So does a function a `//go:linkname` or
  `//export` comment in its own file names. A `//go:linkname` in another
  package that pulls the function in by its full name is not looked for. The
  tool matches names, not types, so a function whose name is also a variable
  or field somewhere it looks keeps its bugs too.

A `//mutate:ignore` comment written to skip one of these bugs now skips
nothing, so the run stops and says why. Delete the comment.

## What stays the same between releases

Other repositories call coho-mutate-go from their checks, so these parts of it
are its interface, and a release changes them only as this section says.

- **The command line.** The flags `-workers`, `-since` and `-version`, and the
  two arguments `<package-dir>` and `<report-file>`, in that order.
- **The exit codes.**
  - 0: the report is written. With `-version`, the version is printed.
  - 1: the run could not finish.
  - 2: the command line could not be read.
- **The report.** It follows version 2 of Stryker's
  [mutation-testing-elements](https://github.com/stryker-mutator/mutation-testing-elements)
  report schema. Each planted bug in it has one of these statuses: Killed,
  Survived, NoCoverage, Timeout, CompileError, RuntimeError or Ignored. Adding
  a status counts as a breaking change, since a caller may refuse a status it
  doesn't know.
- **`-version`.** It prints the module version Go recorded when the binary was
  built, such as `v0.2.0` for `go install …@v0.2.0`. A build from a checkout
  prints the pseudo-version Go makes from its git commit, and a binary Go
  recorded no version for prints `(devel)`.

The summary the tool prints while it runs is for people to read, and any
release may change it. Programs read the report file, the exit code and the
`-version` output alone.

Releases are numbered v0.x. Each change that breaks anything in the list above
is named in that release's notes. A promise of no breaking changes waits for
v1.

## Where it is built

Each release is one commit in this repository, on top of the release before
it. To build it from a checkout, run `go build -o coho-mutate-go .`, or
`make check` to build it and run every test. The tests write JUnit XML under
`test-results/`.
