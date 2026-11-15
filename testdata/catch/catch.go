// Package catch is a package whose planted bugs are each caught by a known
// set of tests.
package catch

// Add's one bug, minus in place of plus, is caught by TestAdd alone.
func Add(a, b int) int { return a + b }

// Not's one bug, dropping the not, is caught by TestNotTrue and TestNotFalse.
func Not(ok bool) bool { return !ok }

// Join's one bug, minus in place of plus, does not compile on strings.
func Join(a, b string) string { return a + b }
