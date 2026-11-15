package fix

// Positive is the package's one entry point.
func Positive(n int) bool { return n > 0 }

// fixture is what the tests compare against.
func fixture(n int) int { return n + 1 }
