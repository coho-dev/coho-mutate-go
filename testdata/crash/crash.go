// Package crash is a package whose planted bugs panic, hang, survive or are
// reached by no test.
package crash

// At is xs[i], or -1 past the end. Both its bugs index past the end and
// panic, in TestAtEnd, TestAtLater/empty, both cases of TestAtTable and both
// cases in TestAtGroup's group.
func At(xs []int, i int) int {
	if i < len(xs) {
		return xs[i]
	}
	return -1
}

// Max is tested on one pair, which tells only one of its bugs apart.
func Max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Unused is called by no test.
func Unused(x int) bool { return x == 0 }

// Unjoined is called by no test, and its one bug does not compile.
func Unjoined(a, b string) string { return a + b }

// Count counts up to n. Counting i down instead never ends, in each of the
// four TestCount tests, and the tester stops at the first.
func Count(n int) int {
	c := 0
	for i := 0; i < n; {
		c++
		i++
	}
	return c
}
