// Package parallel is a package whose planted bugs exit the test program
// while two parallel tests are running.
package parallel

import "os"

// Check exits the program for a negative n. Its bug "<=" exits for zero too,
// in TestZero and in TestGroup's own body. Its bug ">=" exits for every n, in
// those two, in both of TestSlow's subtests, and in TestPair/b.
func Check(n int) int {
	if n < 0 {
		os.Exit(3)
	}
	return n
}
