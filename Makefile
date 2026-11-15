# coho-mutate-go, a mutation tester for Go (module
# github.com/coho-dev/coho-mutate-go).
.PHONY: build test test-unit test-fast check

build:
	go build -o coho-mutate-go .

# The tests write JUnit XML under RESULTS, one file per test type, and
# `make check RESULTS=<folder>` writes it somewhere else. A test's type is in
# its name: the tests that run a real `go test` on the packages under testdata
# start TestMutate_ and are integration tests. Every other test is a unit test.
# gotestsum comes from go.mod's tool directive.
RESULTS     := test-results
GOTESTSUM   := go tool gotestsum --format standard-quiet
INTEGRATION := ^TestMutate_

test: test-unit
	@mkdir -p $(RESULTS)/integration
	$(GOTESTSUM) --junitfile $(RESULTS)/integration/mutate.xml -- -run '$(INTEGRATION)' ./...

# test-fast is the unit tests alone, quick enough to run after every edit.
test-fast: test-unit

test-unit:
	@mkdir -p $(RESULTS)/unit
	$(GOTESTSUM) --junitfile $(RESULTS)/unit/mutate.xml -- -skip '$(INTEGRATION)' ./...

check: build
	$(MAKE) test
	@echo "→ mutate gate: ok"
