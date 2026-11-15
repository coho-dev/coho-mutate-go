// Package cases is a package with bugs in switch and select case
// expressions, in a function a test calls and in ones no test does. Go's
// coverage profile gives a case expression no block of its own.
package cases

// limit's bug is in a package-level declaration, which no coverage block
// holds, so it is run against the tests.
var limit = 8 + 1

// Size's bug in its case is caught by TestSize.
func Size(n int) string {
	switch {
	case n > limit:
		return "big"
	}
	return "small"
}

// Unused's bug in its case is in a function no test calls.
func Unused(n int) string {
	switch {
	case n == 3:
		return "three"
	}
	return ""
}

// Send's bug in its case is in a function no test calls.
func Send(c chan int, n int) bool {
	select {
	case c <- n * 2:
		return true
	default:
		return false
	}
}
