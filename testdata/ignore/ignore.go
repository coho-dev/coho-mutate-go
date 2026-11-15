// Package ignore is a package with a bug the tester is told to skip.
package ignore

// Count counts up to n.
func Count(n int) int {
	c := 0
	for i := 0; i < n; {
		c++
		//mutate:ignore UpdateOperator: counting i down never ends
		i++
	}
	return c
}
