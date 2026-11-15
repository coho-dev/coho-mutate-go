package parallel

import (
	"testing"
	"time"
)

// TestSlow is still running, before its subtests start, when TestZero exits
// the program, though it catches only one of Check's bugs. Run again alone,
// its first subtest exits the program before its second runs.
func TestSlow(t *testing.T) {
	t.Parallel()
	time.Sleep(300 * time.Millisecond)
	t.Run("five", func(t *testing.T) {
		if got := Check(5); got != 5 {
			t.Errorf("Check(5) = %d", got)
		}
	})
	t.Run("six", func(t *testing.T) {
		if got := Check(6); got != 6 {
			t.Errorf("Check(6) = %d", got)
		}
	})
}

func TestZero(t *testing.T) {
	t.Parallel()
	if got := Check(0); got != 0 {
		t.Errorf("Check(0) = %d", got)
	}
}

// TestPair's subtests run in parallel. Its bug ">=" exits in b while a is
// still sleeping, and a calls nothing, so only b caught it.
func TestPair(t *testing.T) {
	t.Parallel()
	t.Run("a", func(t *testing.T) {
		t.Parallel()
		time.Sleep(300 * time.Millisecond)
	})
	t.Run("b", func(t *testing.T) {
		t.Parallel()
		if got := Check(5); got != 5 {
			t.Errorf("Check(5) = %d", got)
		}
	})
}

// TestGroup's own body exits the program on Check's bugs, after its subtest
// has run, and the subtest calls nothing.
func TestGroup(t *testing.T) {
	t.Run("ok", func(t *testing.T) {})
	if got := Check(0); got != 0 {
		t.Errorf("Check(0) = %d", got)
	}
}
